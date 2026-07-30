package preprocessor

import (
	"strings"
	"unicode"
)

// CommentStripper removes SQL comments while preserving string literals.
type CommentStripper interface {
	// Strip removes SQL comments. String literals (single-quoted and
	// dollar-quoted) are preserved verbatim.
	Strip(sql string) string

	// RedactForMacros returns a length-preserved mask of the input where
	// bytes inside comments, string literals, and quoted identifiers are
	// replaced with ASCII spaces. All other bytes stay at their original
	// positions.
	//
	// The macro detector runs on this mask so tokens like
	// "CALL pgmi_test();" that appear inside single-quoted or
	// dollar-quoted literals, quoted identifiers, or comments are invisible
	// to it. Byte offsets returned by the detector are directly usable
	// against the ORIGINAL SQL because the mask preserves length.
	RedactForMacros(sql string) string

	// BlockComments returns the byte span of every top-level /* ... */ comment,
	// delimiters included, in source order. Nested comments belong to their
	// outermost span, and a /* inside a string literal, dollar-quoted body or
	// quoted identifier is not a comment at all — the same state machine Strip
	// uses decides, so nothing can disagree with it.
	BlockComments(sql string) []CommentSpan
}

// CommentSpan locates one block comment in the original SQL: Start is the
// offset of its '/', End the offset just past its closing '/'.
type CommentSpan struct {
	Start, End int
}

// commentStripper implements CommentStripper using a state machine.
type commentStripper struct{}

// NewCommentStripper creates a new CommentStripper instance.
func NewCommentStripper() CommentStripper {
	return &commentStripper{}
}

// parserState represents the current state of the parser.
type parserState int

const (
	stateNormal parserState = iota
	stateLineComment
	stateBlockComment
	stateSingleQuote
	stateDollarQuote
	stateQuotedIdentifier
)

// Strip removes SQL comments and returns the stripped SQL.
// Handles:
// - Single-line comments: -- to end of line
// - Block comments: /* */ with PostgreSQL nesting support
// - Single-quoted strings: '...' with doubled-apostrophe and E'...' escapes
// - Dollar-quoted strings: $$...$$ and $tag$...$tag$
// - Quoted identifiers: "..." with doubled-quote escape
func (c *commentStripper) Strip(sql string) string {
	return c.scan(sql, false, nil)
}

// RedactForMacros returns a byte-for-byte length-preserved copy of the input
// with comment and string-literal bytes replaced by ASCII spaces. See the
// CommentStripper interface doc for why this matters.
func (c *commentStripper) RedactForMacros(sql string) string {
	return c.scan(sql, true, nil)
}

// BlockComments walks the SQL with the same state machine as Strip and reports
// where each top-level block comment begins and ends.
func (c *commentStripper) BlockComments(sql string) []CommentSpan {
	var spans []CommentSpan
	c.scan(sql, false, &spans)
	return spans
}

// scan walks the SQL with the same state machine used by Strip. When
// lengthPreserve is false (Strip), comment bytes are dropped and string
// literals are written verbatim. When lengthPreserve is true
// (RedactForMacros), comments and string-literal bodies are replaced with
// spaces byte-for-byte so positions in the output align with positions in
// the input.
//
// All comparisons are ASCII ('$', the escaped single quote, '"', '\\', '/', '*', '-', '\n', '\r');
// multi-byte UTF-8 continuation bytes flow through the default branches
// unchanged, which is what we want (they are all inside either a string, a
// comment, or an identifier — they never affect state transitions).
// spans, when non-nil, collects the byte span of every top-level block
// comment as the walk discovers them.
func (c *commentStripper) scan(sql string, lengthPreserve bool, spans *[]CommentSpan) string {
	if len(sql) == 0 {
		return ""
	}

	var result strings.Builder
	result.Grow(len(sql))

	// writeSpaces emits n ASCII spaces (used only in length-preserve mode).
	writeSpaces := func(n int) {
		for range n {
			result.WriteByte(' ')
		}
	}

	state := stateNormal
	blockDepth := 0
	dollarTag := ""
	escapeString := false

	runes := []rune(sql)
	// Byte widths of each rune — needed so length-preserving mode writes the
	// correct number of placeholder spaces for multi-byte runes.
	runeBytes := make([]int, len(runes))
	// byteAt[idx] is the byte offset of runes[idx]; the walk advances i by 1 or
	// 2 in a dozen branches, so deriving the offset beats incrementing it.
	byteAt := make([]int, len(runes)+1)
	for idx, r := range runes {
		runeBytes[idx] = runeByteLen(r)
		byteAt[idx+1] = byteAt[idx] + runeBytes[idx]
	}

	i := 0
	commentStart := 0
	for i < len(runes) {
		r := runes[i]
		rw := runeBytes[i]
		bytePos := byteAt[i]
		var next rune
		var nextW int
		if i+1 < len(runes) {
			next = runes[i+1]
			nextW = runeBytes[i+1]
		}

		switch state {
		case stateNormal:
			if r == '-' && next == '-' {
				state = stateLineComment
				if lengthPreserve {
					writeSpaces(rw + nextW)
				}
				i += 2
			} else if r == '/' && next == '*' {
				state = stateBlockComment
				blockDepth = 1
				commentStart = bytePos
				if lengthPreserve {
					writeSpaces(rw + nextW)
				}
				i += 2
			} else if r == '\'' {
				state = stateSingleQuote
				escapeString = escapeStringPrefixed(runes, i)
				result.WriteRune(r)
				i++
			} else if r == '"' {
				state = stateQuotedIdentifier
				result.WriteRune(r)
				i++
			} else if r == '$' {
				tag := c.extractDollarTag(runes, i)
				if tag != "" {
					state = stateDollarQuote
					dollarTag = tag
					result.WriteString(tag)
					i += len([]rune(tag))
				} else {
					result.WriteRune(r)
					i++
				}
			} else {
				result.WriteRune(r)
				i++
			}

		case stateLineComment:
			if r == '\n' {
				result.WriteRune(r)
				state = stateNormal
				i++
			} else if r == '\r' && next == '\n' {
				result.WriteRune(r)
				result.WriteRune(next)
				state = stateNormal
				i += 2
			} else {
				if lengthPreserve {
					writeSpaces(rw)
				}
				i++
			}

		case stateBlockComment:
			if r == '/' && next == '*' {
				blockDepth++
				if lengthPreserve {
					writeSpaces(rw + nextW)
				}
				i += 2
			} else if r == '*' && next == '/' {
				blockDepth--
				if lengthPreserve {
					writeSpaces(rw + nextW)
				}
				i += 2
				if blockDepth == 0 {
					state = stateNormal
					if spans != nil {
						*spans = append(*spans, CommentSpan{Start: commentStart, End: bytePos + rw + nextW})
					}
				}
			} else {
				if lengthPreserve {
					writeSpaces(rw)
				}
				i++
			}

		case stateSingleQuote:
			if escapeString && r == '\\' && i+1 < len(runes) {
				// Only in E'...' does a backslash escape the next character, so
				// an escaped apostrophe must not be read as the closing quote.
				if lengthPreserve {
					writeSpaces(rw + nextW)
				} else {
					result.WriteRune(r)
					result.WriteRune(next)
				}
				i += 2
			} else if r == '\'' {
				if next == '\'' {
					// Escaped quote — preserve both bytes verbatim so macro
					// detection cannot misinterpret them as delimiters.
					result.WriteRune(r)
					result.WriteRune(next)
					i += 2
				} else {
					// Closing quote — write verbatim.
					result.WriteRune(r)
					state = stateNormal
					i++
				}
			} else {
				if lengthPreserve {
					writeSpaces(rw)
				} else {
					result.WriteRune(r)
				}
				i++
			}

		case stateQuotedIdentifier:
			if r == '"' {
				if next == '"' {
					result.WriteRune(r)
					result.WriteRune(next)
					i += 2
				} else {
					result.WriteRune(r)
					state = stateNormal
					i++
				}
			} else {
				if lengthPreserve {
					writeSpaces(rw)
				} else {
					result.WriteRune(r)
				}
				i++
			}

		case stateDollarQuote:
			if c.matchesDollarTag(runes, i, dollarTag) {
				result.WriteString(dollarTag)
				i += len([]rune(dollarTag))
				state = stateNormal
				dollarTag = ""
			} else {
				if lengthPreserve {
					writeSpaces(rw)
				} else {
					result.WriteRune(r)
				}
				i++
			}
		}
	}

	// An unterminated /* runs to EOF. Strip drops those bytes and
	// RedactForMacros blanks them, so BlockComments must report the span or the
	// three disagree about the same input — the divergence that let a third
	// scanner in internal/metadata drift unnoticed.
	if spans != nil && state == stateBlockComment {
		*spans = append(*spans, CommentSpan{Start: commentStart, End: len(sql)})
	}

	return result.String()
}

// escapeStringPrefixed reports whether the quote at runes[i] opens a C-style
// escape string. With standard_conforming_strings on — the default since 9.1 —
// a backslash is literal in an ordinary '...' literal and only escapes inside
// E'...'. The E must be a whole token prefix: in abcE'x' the lexer reads the
// identifier abcE and then an ordinary literal, so the backslash stays literal.
func escapeStringPrefixed(runes []rune, i int) bool {
	if i == 0 || (runes[i-1] != 'E' && runes[i-1] != 'e') {
		return false
	}
	return i == 1 || !isIdentifierRune(runes[i-2])
}

// isIdentifierRune reports whether r may appear inside an unquoted SQL
// identifier after its first character.
func isIdentifierRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// runeByteLen returns the UTF-8 byte length of a rune (1..4 for valid runes).
func runeByteLen(r rune) int {
	switch {
	case r < 0x80:
		return 1
	case r < 0x800:
		return 2
	case r < 0x10000:
		return 3
	default:
		return 4
	}
}

// extractDollarTag extracts a dollar-quote tag starting at position i.
// Returns the full tag (e.g., "$$" or "$tag$") or empty string if not a valid tag.
func (c *commentStripper) extractDollarTag(runes []rune, i int) string {
	if i >= len(runes) || runes[i] != '$' {
		return ""
	}

	j := i + 1
	for j < len(runes) {
		r := runes[j]
		if r == '$' {
			return string(runes[i : j+1])
		}
		if j == i+1 {
			// Tag identifier must start with letter or underscore (PostgreSQL spec)
			if !unicode.IsLetter(r) && r != '_' {
				return ""
			}
		} else {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
				return ""
			}
		}
		j++
	}

	return ""
}

// matchesDollarTag checks if the runes starting at position i match the given dollar tag.
func (c *commentStripper) matchesDollarTag(runes []rune, i int, tag string) bool {
	tagRunes := []rune(tag)
	if i+len(tagRunes) > len(runes) {
		return false
	}

	for j, tr := range tagRunes {
		if runes[i+j] != tr {
			return false
		}
	}
	return true
}
