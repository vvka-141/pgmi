package pgmi

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/internal/files/pathrule"
)

// Sentinel errors for common failure scenarios.
// These enable callers to distinguish error types using errors.Is().
//
// Example usage:
//
//	err := deployer.Deploy(ctx, config)
//	if errors.Is(err, pgmi.ErrApprovalDenied) {
//	    // Handle user denying approval
//	}
var (
	// ErrInvalidConfig indicates the provided configuration is invalid.
	ErrInvalidConfig = errors.New("invalid configuration")

	// ErrDeploySQLNotFound indicates the required deploy.sql file was not found.
	ErrDeploySQLNotFound = errors.New("deploy.sql not found")

	// ErrApprovalDenied indicates the user denied approval for the operation.
	ErrApprovalDenied = errors.New("approval denied")

	// ErrExecutionFailed indicates SQL execution failed.
	ErrExecutionFailed = errors.New("execution failed")

	// ErrUnsupportedAuthMethod indicates the requested authentication method is not supported.
	ErrUnsupportedAuthMethod = errors.New("unsupported authentication method")

	// ErrConnectionFailed indicates database connection failed.
	ErrConnectionFailed = errors.New("connection failed")

	// ErrConcurrentDeploy indicates another pgmi deployment is already in
	// progress against the target database (Go-side advisory lock contention).
	ErrConcurrentDeploy = errors.New("concurrent deployment in progress")

	// ErrUsage indicates a CLI usage error (missing args, invalid flags,
	// unknown commands/templates). Wraps Cobra and pgmi validation errors
	// at the boundary where the intent is known — ExitCodeForError checks
	// errors.Is instead of sniffing error prose.
	ErrUsage = errors.New("usage error")
)

// ExitCodeForError returns the appropriate exit code for an error.
// Returns ExitSuccess (0) for nil errors, semantic codes for known errors,
// and ExitGeneralError (1) for unclassified errors.
func ExitCodeForError(err error) int {
	if err == nil {
		return ExitSuccess
	}

	// SIGINT / Ctrl-C produces context.Canceled when the deploy goroutine
	// observes the cancelled context. Map to 130 per Unix convention (128 + SIGINT).
	if errors.Is(err, context.Canceled) {
		return ExitInterrupted
	}

	// Check for sentinel errors BEFORE context.DeadlineExceeded: pgx's dial
	// timeout wraps DeadlineExceeded, but pgmi tags the error with
	// ErrConnectionFailed — that sentinel must win so a connect timeout
	// exits 11 (connection) not 16 (--timeout).
	switch {
	case errors.Is(err, ErrConcurrentDeploy):
		return ExitConcurrentDeploy
	case errors.Is(err, ErrInvalidConfig):
		return ExitConfigError
	case errors.Is(err, ErrDeploySQLNotFound):
		return ExitDeploySQLMissing
	case errors.Is(err, ErrApprovalDenied):
		return ExitApprovalDenied
	case errors.Is(err, ErrConnectionFailed):
		return ExitConnectionError
	case errors.Is(err, ErrUnsupportedAuthMethod):
		return ExitConfigError
	}

	// --timeout expiry. A connect timeout is already handled above, because
	// pgx's dial timeout wraps DeadlineExceeded and must still read as 11.
	//
	// This sits BEFORE ErrExecutionFailed on purpose. deploy.sql outliving
	// --timeout produces ErrExecutionFailed wrapping DeadlineExceeded, and
	// reporting that as 13 tells CI the script is broken when it is not: the
	// remedy is a longer --timeout or a faster deploy, not a fix to the SQL.
	// PostgreSQL's own statement_timeout keeps 13, and should — it arrives as
	// SQLSTATE 57014 with no Go deadline anywhere in the chain.
	if errors.Is(err, context.DeadlineExceeded) {
		return ExitTimeout
	}

	if errors.Is(err, ErrExecutionFailed) {
		return ExitExecutionFailed
	}

	if errors.Is(err, ErrUsage) {
		return ExitUsageError
	}

	return ExitGeneralError
}

// ScriptError carries the script pgmi actually sent to PostgreSQL alongside the
// error it produced. PgError.Position is an offset into that exact text, so
// without it a position cannot be turned into a line number.
//
// Script is the preprocessed text: when deploy.sql contains pgmi_test() macros,
// it is not byte-for-byte the file on disk, and Expanded records that so the
// user is never handed a line number that silently disagrees with their editor.
//
// Files are the project files deploy.sql could have EXECUTEd. An error raised
// inside one carries that file's text, as InternalQuery for parse and analysis
// errors or as the `SQL statement "..."` context line for runtime errors, so
// the failing file is found by its content rather than by message wording.
type ScriptError struct {
	Err      error
	Name     string
	Script   string
	Expanded bool
	Files    []FileMetadata
	// Source is the script on disk when Script is its macro expansion, and
	// OriginalLine maps a Script line to a Source line (inMacro when the line
	// came from an expansion). With both set, locations name file lines.
	Source       string
	OriginalLine func(expanded int) (line int, inMacro bool)
	// Tail is true for a statement sent on its own after the first top-level
	// COMMIT, so a runtime error without a position can still be placed.
	Tail bool
}

func (e *ScriptError) Error() string { return e.Err.Error() }
func (e *ScriptError) Unwrap() error { return e.Err }

// failedFiles returns the SQL project files whose text PostgreSQL was
// executing when pgErr was raised: one, or several when files share that text.
func (e *ScriptError) failedFiles(pgErr *pgconn.PgError) []*FileMetadata {
	text := ""
	if pgErr.InternalQuery != "" {
		for i := range e.Files {
			if e.Files[i].isSQL() && e.Files[i].Content == pgErr.InternalQuery {
				text = pgErr.InternalQuery
				break
			}
		}
	}
	if text == "" {
		for i := range e.Files {
			c := e.Files[i].Content
			if !e.Files[i].isSQL() || strings.TrimSpace(c) == "" || len(c) <= len(text) {
				continue
			}
			if strings.Contains(pgErr.Where, `SQL statement "`+c+`"`) {
				text = c
			}
		}
	}
	if text == "" {
		return nil
	}
	var matches []*FileMetadata
	for i := range e.Files {
		if e.Files[i].isSQL() && e.Files[i].Content == text {
			matches = append(matches, &e.Files[i])
		}
	}
	return matches
}

func (f *FileMetadata) isSQL() bool {
	return pathrule.IsSQLExtension(path.Ext(f.Path))
}

func (e *ScriptError) failedFile(pgErr *pgconn.PgError) *FileMetadata {
	if files := e.failedFiles(pgErr); len(files) > 0 {
		return files[0]
	}
	return nil
}

// SQLLocation is a PostgreSQL error position resolved against the executed script.
type SQLLocation struct {
	Script     string
	Line       int
	Column     int
	SourceLine string
	Expanded   bool
}

// LocateError resolves a PgError.Position to a line and column in the script
// pgmi executed. Returns nil unless the chain carries both a *ScriptError and a
// *pgconn.PgError with a position (PostgreSQL omits it for most runtime errors;
// syntax errors always carry it).
func LocateError(err error) *SQLLocation {
	if err == nil {
		return nil
	}

	var scriptErr *ScriptError
	if !errors.As(err, &scriptErr) {
		return nil
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}

	if pgErr.Position <= 0 {
		if f := scriptErr.failedFile(pgErr); f != nil {
			// InternalQuery is the whole file for a parse error in it, or a
			// routine body inside it for one in CREATE FUNCTION.
			if idx := strings.Index(f.Content, pgErr.InternalQuery); pgErr.InternalPosition > 0 && pgErr.InternalQuery != "" && idx >= 0 {
				offset := utf8.RuneCountInString(f.Content[:idx])
				line, column, sourceLine, ok := resolvePosition(f.Content, offset+int(pgErr.InternalPosition))
				if !ok {
					return nil
				}
				return &SQLLocation{Script: f.Path, Line: line, Column: column, SourceLine: sourceLine}
			}
			line := fileLine(f.Content, pgErr.Where)
			if line == 0 || line > strings.Count(f.Content, "\n")+1 {
				return nil
			}
			loc := &SQLLocation{Script: f.Path, Line: line}
			if lines := strings.Split(f.Content, "\n"); line <= len(lines) {
				loc.SourceLine = strings.TrimRight(lines[line-1], "\r")
			}
			return loc
		}
		if scriptErr.Tail {
			return scriptErr.locate(tailLine(scriptErr.Script, pgErr.Where), 0)
		}
		return scriptErr.locateHead(pgErr.Where)
	}

	line, column, _, ok := resolvePosition(scriptErr.Script, int(pgErr.Position))
	if !ok {
		return nil
	}
	return scriptErr.locate(line, column)
}

// locate builds a location for a line of the executed script, mapped back to
// the file on disk when the script was a macro expansion.
func (e *ScriptError) locate(line, column int) *SQLLocation {
	text := e.Script
	expanded := e.Expanded
	if e.OriginalLine != nil && e.Source != "" {
		var inMacro bool
		line, inMacro = e.OriginalLine(line)
		if inMacro {
			column = 0
		}
		text, expanded = e.Source, false
	}
	loc := &SQLLocation{Script: e.Name, Line: line, Column: column, Expanded: expanded}
	if lines := strings.Split(text, "\n"); line >= 1 && line <= len(lines) {
		loc.SourceLine = strings.TrimRight(lines[line-1], "\r")
	}
	return loc
}

var (
	doBlockStart   = regexp.MustCompile(`(?is)^DO\s+(?:LANGUAGE\s+\w+\s+)?\$\w*\$`)
	doBlockLine    = regexp.MustCompile(`PL/pgSQL function inline_code_block line (\d+)`)
	doBlockAtStart = regexp.MustCompile(`(?im)^[ \t]*DO\s+(?:LANGUAGE\s+\w+\s+)?\$\w*\$`)
)

// locateHead places a runtime error in the atomic head, which PostgreSQL
// reports without a position: in deploy.sql always, and on a line when the
// head holds exactly one DO block and the error was raised inside it.
func (e *ScriptError) locateHead(where string) *SQLLocation {
	blocks := doBlockAtStart.FindAllStringIndex(e.Script, -1)
	if len(blocks) != 1 || !doBlockLine.MatchString(where) {
		return &SQLLocation{Script: e.Name}
	}
	start := blocks[0][0]
	return e.locate(strings.Count(e.Script[:start], "\n")+doBlockErrorLine(e.Script[start:], where, true), 0)
}

// doBlockErrorLine is the line of unit, a DO statement, that where's PL/pgSQL
// line falls on; 0 when unit is no DO block or where names none. A block run
// from another one is the first inline_code_block in where, the outermost the
// last.
func doBlockErrorLine(unit, where string, outermost bool) int {
	start := statementStart(unit)
	matches := doBlockLine.FindAllStringSubmatch(where, -1)
	if !doBlockStart.MatchString(unit[start:]) || len(matches) == 0 {
		return 0
	}
	m := matches[0]
	if outermost {
		m = matches[len(matches)-1]
	}
	body, err := strconv.Atoi(m[1])
	if err != nil || body < 1 {
		return 0
	}
	tag := strings.IndexByte(unit[start:], '$')
	return strings.Count(unit[:start+tag], "\n") + body
}

// fileLine places a runtime error inside a file's own DO block. where lists
// frames innermost first, and the statement that ran the file (deploy.sql's
// EXECUTE) quotes it: only the frames before that quote are the file's. With
// none, the error is in a plain statement of the file, which has no line.
func fileLine(content, where string) int {
	if i := strings.Index(where, `SQL statement "`+content); i >= 0 {
		return doBlockErrorLine(content, where[:i], true)
	}
	return doBlockErrorLine(content, where, false)
}

// tailLine places a runtime error in a tail statement, which PostgreSQL
// reports without a position: the statement's first line, or for a DO block
// its opening line plus the PL/pgSQL line of the outermost block in where.
func tailLine(unit, where string) int {
	if line := doBlockErrorLine(unit, where, true); line > 0 {
		return line
	}
	start := statementStart(unit)
	return strings.Count(unit[:start], "\n") + 1
}

// statementStart is the offset of unit's first statement: past whitespace,
// -- comments and (nested) /* */ comments, the header most files open with.
func statementStart(unit string) int {
	i := 0
	for i < len(unit) {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(unit[i])):
			i++
		case strings.HasPrefix(unit[i:], "--"):
			end := strings.IndexByte(unit[i:], '\n')
			if end < 0 {
				return len(unit)
			}
			i += end + 1
		case strings.HasPrefix(unit[i:], "/*"):
			depth := 0
			for i < len(unit) {
				if strings.HasPrefix(unit[i:], "/*") {
					depth, i = depth+1, i+2
				} else if strings.HasPrefix(unit[i:], "*/") {
					depth, i = depth-1, i+2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
		default:
			return i
		}
	}
	return i
}

// resolvePosition converts a PostgreSQL error position into a line and column.
// Per the protocol, the position is 1-based and counted in characters, not
// bytes — so a multi-byte identifier earlier in the script would skew a
// byte-indexed walk.
func resolvePosition(script string, position int) (line, column int, sourceLine string, ok bool) {
	runes := []rune(script)
	if position > len(runes)+1 {
		return 0, 0, "", false
	}

	line, column = 1, 1
	for i := 0; i < position-1; i++ {
		if runes[i] == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}

	lines := strings.Split(script, "\n")
	if line <= len(lines) {
		sourceLine = strings.TrimRight(lines[line-1], "\r")
	}

	return line, column, sourceLine, true
}

// FormatError renders an error for CLI output. For plain errors it returns the
// message. If the chain contains a *pgconn.PgError, it appends the DETAIL,
// HINT, and WHERE context fields that PostgreSQL attached to the error but
// that err.Error() omits, matching the diagnostic fields psql surfaces. When the
// error carries a position, it also points at the offending line psql-style.
//
// Password material embedded in connection strings is scrubbed before return:
// any `password=<value>` query-style fragment or `user:<password>@` URI
// fragment is replaced with a `[redacted]` marker. pgmi today does not leak
// passwords into its own errors, but defence in depth is cheap.
func FormatError(err error) string {
	if err == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString(message(err))

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Detail != "" {
			fmt.Fprintf(&b, "\nDETAIL: %s", pgErr.Detail)
		}
		if pgErr.Hint != "" {
			fmt.Fprintf(&b, "\nHINT: %s", pgErr.Hint)
		}
		if pgErr.Where != "" {
			fmt.Fprintf(&b, "\nWHERE: %s", namedWhere(err, pgErr.Where))
		}
	}

	if loc := LocateError(err); loc != nil && loc.Line == 0 {
		fmt.Fprintf(&b, "\nLOCATION: %s", loc.Script)
	} else if loc != nil {
		fmt.Fprintf(&b, "\nLOCATION: %s line %d", loc.Script, loc.Line)
		if loc.Column > 0 {
			fmt.Fprintf(&b, ", column %d", loc.Column)
		}
		if loc.Expanded {
			fmt.Fprintf(&b, " (of the expanded script: pgmi_test() macros shift line numbers relative to the file on disk)")
		}
		if loc.SourceLine != "" {
			prefix := fmt.Sprintf("LINE %d: ", loc.Line)
			fmt.Fprintf(&b, "\n%s%s", prefix, loc.SourceLine)
			if loc.Column > 0 {
				fmt.Fprintf(&b, "\n%s^", strings.Repeat(" ", utf8.RuneCountInString(prefix)+loc.Column-1))
			}
		}
	} else if f := FailedFile(err); f != "" {
		fmt.Fprintf(&b, "\nLOCATION: %s", f)
	}
	if others := FailedFileCandidates(err); len(others) > 1 {
		fmt.Fprintf(&b, "\nThe same text is in %s; the error is in whichever of them deploy.sql ran.", strings.Join(others, ", "))
	}

	return redactPasswords(b.String())
}

// namedWhere replaces the text of a loaded project file quoted in PostgreSQL's
// context with the file's path: the reader has the file, and a test file
// printed in full buries the error.
func namedWhere(err error, where string) string {
	var scriptErr *ScriptError
	if !errors.As(err, &scriptErr) {
		return where
	}
	for _, f := range scriptErr.Files {
		if !f.isSQL() || strings.TrimSpace(f.Content) == "" {
			continue
		}
		where = strings.ReplaceAll(where, `SQL statement "`+f.Content+`"`, "SQL statement in "+f.Path)
	}
	return where
}

// ErrorDetail is the machine-readable form of a failed operation, carrying the
// PostgreSQL diagnostic fields that err.Error() omits. All fields are
// password-redacted and safe to emit on --json output or MCP structuredContent.
type ErrorDetail struct {
	Message    string `json:"message"`
	SQLState   string `json:"sqlstate,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Hint       string `json:"hint,omitempty"`
	Where      string `json:"where,omitempty"`
	FailedFile string `json:"failedFile,omitempty"`
	// Phase is "test" when the failure is in a test or fixture under
	// __test__/ or __tests__/, "deploy" for any other failure of deploy.sql.
	Phase    string `json:"phase,omitempty"`
	ExitCode int    `json:"exitCode"`

	// FailedFileCandidates lists every SQL file holding the failing text when
	// more than one does; FailedFile is the first of them.
	FailedFileCandidates []string `json:"failedFileCandidates,omitempty"`

	// Location of the error in the script pgmi executed. Script/Line/Column are
	// absent unless PostgreSQL reported a position (syntax errors always do).
	// ScriptExpanded true means the line refers to the macro-expanded script, not
	// the file on disk.
	Script         string `json:"script,omitempty"`
	Line           int    `json:"line,omitempty"`
	Column         int    `json:"column,omitempty"`
	SourceLine     string `json:"sourceLine,omitempty"`
	ScriptExpanded bool   `json:"scriptExpanded,omitempty"`
}

// failedFilePattern extracts the file path from a handler that re-raises as
// RAISE EXCEPTION 'Failed in %: %', path, SQLERRM. Such a handler discards the
// text PostgreSQL was executing, so the message is the only attribution left.
var failedFilePattern = regexp.MustCompile(`Failed in (\S+\.sql)`)

// FailedFile names the project file an error was raised in: by the file text
// PostgreSQL reports, else by the `Failed in <path>:` message convention.
// Returns "" when neither identifies one.
func FailedFile(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	var scriptErr *ScriptError
	if errors.As(err, &scriptErr) {
		if f := scriptErr.failedFile(pgErr); f != nil {
			return f.Path
		}
	}
	if m := failedFilePattern.FindStringSubmatch(pgErr.Message); m != nil {
		return m[1]
	}
	return ""
}

// FailedFileCandidates names every SQL file holding the text PostgreSQL was
// executing, when the file text identifies it. Nil otherwise.
func FailedFileCandidates(err error) []string {
	var pgErr *pgconn.PgError
	var scriptErr *ScriptError
	if !errors.As(err, &pgErr) || !errors.As(err, &scriptErr) {
		return nil
	}
	var paths []string
	for _, f := range scriptErr.failedFiles(pgErr) {
		paths = append(paths, f.Path)
	}
	return paths
}

// message is err's text without the ": invalid configuration" a wrap leaves
// at the end: the exit code already says it, and after a multi-line hint it
// reads as part of the hint.
// sentinels name the exit-code category; the code already carries it, so
// message drops them from the text, wherever a wrap put them.
var sentinels = []error{ErrInvalidConfig, ErrDeploySQLNotFound, ErrApprovalDenied, ErrExecutionFailed,
	ErrUnsupportedAuthMethod, ErrConnectionFailed, ErrConcurrentDeploy, ErrUsage}

func message(err error) string {
	text := err.Error()
	if errors.Is(err, context.Canceled) && strings.Contains(text, context.Canceled.Error()) {
		return "interrupted"
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		for stripped := true; stripped; {
			stripped = false
			for _, s := range sentinels {
				if t, ok := strings.CutPrefix(line, s.Error()+": "); ok {
					line, stripped = t, true
				}
				if t, ok := strings.CutSuffix(line, ": "+s.Error()); ok {
					line, stripped = t, true
				}
			}
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// WithMessage gives err the text a person reads and keeps err in the chain,
// so errors.Is and ExitCodeForError see what they saw before.
func WithMessage(err error, msg string) error {
	return reworded{msg: msg, err: err}
}

type reworded struct {
	msg string
	err error
}

func (r reworded) Error() string { return r.msg }
func (r reworded) Unwrap() error { return r.err }

// NewErrorDetail extracts structured diagnostics from an error chain.
// Returns nil for a nil error.
func NewErrorDetail(err error) *ErrorDetail {
	if err == nil {
		return nil
	}
	d := &ErrorDetail{
		Message:  redactPasswords(message(err)),
		ExitCode: ExitCodeForError(err),
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		d.SQLState = pgErr.Code
		d.Detail = redactPasswords(pgErr.Detail)
		d.Hint = redactPasswords(pgErr.Hint)
		d.Where = redactPasswords(namedWhere(err, pgErr.Where))
		d.FailedFile = FailedFile(err)
		if c := FailedFileCandidates(err); len(c) > 1 {
			d.FailedFileCandidates = c
		}
	}
	if loc := LocateError(err); loc != nil {
		d.Script = loc.Script
		d.Line = loc.Line
		d.Column = loc.Column
		d.SourceLine = redactPasswords(loc.SourceLine)
		d.ScriptExpanded = loc.Expanded
		if d.FailedFile == "" {
			d.FailedFile = loc.Script
		}
	}
	var scriptErr *ScriptError
	if errors.As(err, &scriptErr) {
		d.Phase = "deploy"
		if pathrule.IsTestPath(d.FailedFile) {
			d.Phase = "test"
		}
	}
	return d
}

// passwordKVPattern matches the keyword form: `password=secret`, `sslpassword=x`,
// ADO.NET's `Pwd=`, and quoted values in both escape styles: ADO.NET doubles
// the quote, libpq escapes it with a backslash. The unquoted
// value ends at whitespace, `&` or `;` — the separators of the URI query and
// ADO.NET forms respectively.
var passwordKVPattern = regexp.MustCompile(`(?i)(?:password|\bpwd)\s*=\s*('(?:[^'\\]|''|\\.)*'|"(?:[^"\\]|""|\\.)*"|[^\s&;'"]*)`)

// passwordURIPattern matches `scheme://user:password@host`. The password runs to
// the LAST `@` in the token, not the first: an unescaped `@` inside a password
// is exactly what makes url.Parse fail, and its error embeds the raw string, so
// the first-`@` reading published everything after it verbatim. Go's own
// parseAuthority splits on the last `@` too, so this agrees with the parser
// whose failure produced the message.
// passwordSQLPattern matches the SQL literal of CREATE/ALTER ROLE ... PASSWORD,
// which a runtime error inside EXECUTE carries in its statement CONTEXT.
// format('%L') writes an E” string when the value holds a backslash.
var passwordSQLPattern = regexp.MustCompile(`(?i)\bPASSWORD\s+(?:E'(?:''|\\.|[^'\\])*'|'(?:''|[^'])*')`)

var passwordURIPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://[^:/@\s]+):[^\s]*@`)

// redactPasswords replaces password-looking substrings with `[redacted]`.
// Handles the libpq keyword form (`password=secret`), the ADO.NET quoted form
// (`Password='p ass'`), the SQL form (`PASSWORD 'secret'`), and the URI form (`postgresql://user:secret@host/db`).
//
// The keyword pass runs first so a `sslpassword=` inside a URI query string is
// consumed before the greedy URI pass could anchor on an `@` within it.
func redactPasswords(s string) string {
	s = passwordKVPattern.ReplaceAllString(s, "password=[redacted]")
	s = passwordSQLPattern.ReplaceAllString(s, "PASSWORD [redacted]")
	s = passwordURIPattern.ReplaceAllString(s, "$1:[redacted]@")
	return s
}
