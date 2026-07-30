package preprocessor

import "strings"

// SplitExecutionUnits splits a preprocessed deploy.sql into the sequence of
// simple-query messages to execute on one connection.
//
// PostgreSQL runs a multi-statement simple query inside an implicit
// transaction block, so a statement like CREATE INDEX CONCURRENTLY can never
// run from a script shipped as one message — even after an explicit COMMIT,
// the remaining statements sit in a fresh implicit block. The contract, in
// one sentence: before the first top-level COMMIT pgmi's atomic mode; after
// it, psql mode.
//
//   - Head: everything up to and including the FIRST top-level transaction
//     terminator (COMMIT / END / ROLLBACK, optionally WORK | TRANSACTION,
//     optionally AND [NO] CHAIN) — one message, preserving today's
//     single-implicit-transaction semantics exactly.
//   - Tail: every top-level statement after that terminator becomes its own
//     message. Per-statement autocommit; an explicit BEGIN ... COMMIT in the
//     tail forms a real transaction because the units share one connection;
//     COMMIT AND CHAIN at the boundary leaves its chained transaction open
//     for the tail.
//   - No top-level terminator: the whole script is one message (unchanged
//     behavior).
//
// ROLLBACK TO [SAVEPOINT] is savepoint control, not a terminator; COMMIT
// PREPARED / ROLLBACK PREPARED act on a foreign prepared transaction, not
// this one — neither anchors the split. A SQL-standard BEGIN ATOMIC function
// body is stepped over whole: its statements are semicolon-separated and its
// END closes the body, not a transaction.
//
// Every tail unit is prefixed with a whitespace mask of everything before it
// (newlines kept, all else spaces), so PostgreSQL error positions and line
// numbers keep resolving against the full script exactly as they do for a
// single message. Terminators inside strings, dollar-quoted bodies, quoted
// identifiers, comments, or parentheses are invisible to the split via
// RedactForMacros.
func SplitExecutionUnits(sql string) []string {
	mask := NewCommentStripper().RedactForMacros(sql)
	// The mask is only length-preserving for valid UTF-8 (invalid bytes become
	// U+FFFD, 3 bytes for 1). Byte offsets from a longer mask would slice out
	// of range, so fall back to a single unit rather than panicking.
	if len(mask) != len(sql) {
		return []string{sql}
	}

	headEnd := -1
	depth := 0
	stmtStart := 0
	chunkStart := 0
	var body atomicBody
	for i := 0; i < len(mask); i++ {
		switch mask[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			if depth == 0 {
				ends := body.observe(mask[chunkStart:i])
				chunkStart = i + 1
				if ends {
					if isTransactionTerminator(mask[stmtStart:i]) {
						headEnd = i + 1
					}
					stmtStart = i + 1
				}
			}
		}
		if headEnd != -1 {
			break
		}
	}
	if headEnd == -1 && isTransactionTerminator(mask[stmtStart:]) {
		headEnd = len(sql)
	}
	if headEnd == -1 || isBlank(mask[headEnd:]) {
		return []string{sql}
	}

	units := []string{sql[:headEnd]}
	depth = 0
	stmtStart = headEnd
	chunkStart = headEnd
	body = atomicBody{}
	for i := headEnd; i < len(mask); i++ {
		switch mask[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			if depth == 0 {
				ends := body.observe(mask[chunkStart:i])
				chunkStart = i + 1
				if ends {
					if !isBlank(mask[stmtStart : i+1]) {
						units = append(units, padPrefix(sql, stmtStart)+sql[stmtStart:i+1])
					}
					stmtStart = i + 1
				}
			}
		}
	}
	if !isBlank(mask[stmtStart:]) {
		units = append(units, padPrefix(sql, stmtStart)+sql[stmtStart:])
	}
	return units
}

// atomicBody tracks whether the scan sits inside a SQL-standard
// BEGIN ATOMIC ... END function body (PostgreSQL 14+). Such a body is not
// dollar-quoted, so RedactForMacros leaves its semicolons and its END visible
// to the split; without this the body's END reads as a COMMIT synonym and
// anchors the head, or in the tail cuts the CREATE FUNCTION into fragments.
type atomicBody struct{ open bool }

// observe consumes the statement ending at a top-level semicolon and reports
// whether that semicolon ends a split-level statement. Semicolons inside a
// body do not; the body's closing END does.
func (b *atomicBody) observe(chunk string) bool {
	if b.open {
		if first, _ := firstWord(chunk); first == "END" {
			b.open = false
			return true
		}
		return false
	}
	rest, found := afterBeginAtomic(chunk)
	if found && !closesBody(rest) {
		b.open = true
		return false
	}
	return true
}

// afterBeginAtomic returns the text following a BEGIN ATOMIC keyword pair.
// The two words must be separated by whitespace alone, so that a query
// selecting columns named begin and atomic does not read as a body.
func afterBeginAtomic(s string) (string, bool) {
	for rest := s; ; {
		word, tail := firstWord(rest)
		if word == "" {
			return "", false
		}
		if word == "BEGIN" && leadingSpaceOnly(tail) {
			if next, after := firstWord(tail); next == "ATOMIC" {
				return after, true
			}
		}
		rest = tail
	}
}

// closesBody reports whether an END unmatched by a CASE appears in s — an
// empty body written as BEGIN ATOMIC END, which opens and closes within one
// statement. CASE ... END is the only other END-terminated construct legal in
// a SQL function body, so pairing the two is enough to tell them apart.
func closesBody(s string) bool {
	depth := 0
	for rest := s; ; {
		word, tail := firstWord(rest)
		switch word {
		case "":
			return false
		case "CASE":
			depth++
		case "END":
			if depth == 0 {
				return true
			}
			depth--
		}
		rest = tail
	}
}

// leadingSpaceOnly reports whether s holds nothing but whitespace before its
// first ASCII letter.
func leadingSpaceOnly(s string) bool {
	for i := 0; i < len(s) && !isASCIILetter(s[i]); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
		default:
			return false
		}
	}
	return true
}

// isTransactionTerminator reports whether a masked top-level statement is a
// transaction terminator. The mask has already blanked strings and comments,
// so plain word extraction is reliable.
func isTransactionTerminator(stmt string) bool {
	first, rest := firstWord(stmt)
	switch first {
	case "COMMIT", "END", "ROLLBACK", "ABORT":
	default:
		return false
	}
	// WORK / TRANSACTION are noise words that may precede TO, so consume them
	// before deciding: ROLLBACK TRANSACTION TO SAVEPOINT s is savepoint
	// control, not a terminator.
	second, after := firstWord(rest)
	if second == "WORK" || second == "TRANSACTION" {
		second, _ = firstWord(after)
	}
	switch second {
	case "", "AND":
		return true
	default:
		// ROLLBACK [WORK|TRANSACTION] TO ... (savepoint control),
		// COMMIT/ROLLBACK PREPARED ... (foreign prepared transaction), or
		// garbage the server will reject.
		return false
	}
}

// firstWord returns the first ASCII-letter word of s uppercased, plus the
// remainder after it.
func firstWord(s string) (string, string) {
	i := 0
	for i < len(s) && !isASCIILetter(s[i]) {
		i++
	}
	j := i
	for j < len(s) && isASCIILetter(s[j]) {
		j++
	}
	word := make([]byte, j-i)
	for k := i; k < j; k++ {
		c := s[k]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		word[k-i] = c
	}
	return string(word), s[j:]
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isBlank(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
		default:
			return false
		}
	}
	return true
}

// padPrefix returns a prefix placing a tail unit on the same line and column it
// occupies in the full script: the newlines that precede end, then one space per
// byte since the last of them.
//
// LocateError derives the line by counting newlines and the column by counting
// characters since the last one, so this resolves identically to masking every
// byte — the spaces on earlier lines were never read. Masking all of them made a
// tail-heavy script quadratic: 4000 statements over 176 KB produced 352 MB of
// units, every byte of it also sent to the server.
func padPrefix(sql string, end int) string {
	head := sql[:end]
	newlines := strings.Count(head, "\n")
	col := end - strings.LastIndexByte(head, '\n') - 1

	pad := make([]byte, newlines+col)
	for i := range newlines {
		pad[i] = '\n'
	}
	for i := newlines; i < len(pad); i++ {
		pad[i] = ' '
	}
	return string(pad)
}
