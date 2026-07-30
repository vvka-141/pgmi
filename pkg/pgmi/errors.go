package pgmi

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
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
type ScriptError struct {
	Err      error
	Name     string
	Script   string
	Expanded bool
}

func (e *ScriptError) Error() string { return e.Err.Error() }
func (e *ScriptError) Unwrap() error { return e.Err }

// NewScriptError attaches an executed script to err. Returns nil for a nil error.
func NewScriptError(err error, name, script string, expanded bool) error {
	if err == nil {
		return nil
	}
	return &ScriptError{Err: err, Name: name, Script: script, Expanded: expanded}
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
	if !errors.As(err, &pgErr) || pgErr.Position <= 0 {
		return nil
	}

	line, column, sourceLine, ok := resolvePosition(scriptErr.Script, int(pgErr.Position))
	if !ok {
		return nil
	}

	return &SQLLocation{
		Script:     scriptErr.Name,
		Line:       line,
		Column:     column,
		SourceLine: sourceLine,
		Expanded:   scriptErr.Expanded,
	}
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
	b.WriteString(err.Error())

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Detail != "" {
			fmt.Fprintf(&b, "\nDETAIL: %s", pgErr.Detail)
		}
		if pgErr.Hint != "" {
			fmt.Fprintf(&b, "\nHINT: %s", pgErr.Hint)
		}
		if pgErr.Where != "" {
			fmt.Fprintf(&b, "\nWHERE: %s", pgErr.Where)
		}
	}

	if loc := LocateError(err); loc != nil {
		fmt.Fprintf(&b, "\nLOCATION: %s line %d, column %d", loc.Script, loc.Line, loc.Column)
		if loc.Expanded {
			fmt.Fprintf(&b, " (of the expanded script: pgmi_test() macros shift line numbers relative to the file on disk)")
		}
		if loc.SourceLine != "" {
			prefix := fmt.Sprintf("LINE %d: ", loc.Line)
			fmt.Fprintf(&b, "\n%s%s", prefix, loc.SourceLine)
			fmt.Fprintf(&b, "\n%s^", strings.Repeat(" ", utf8.RuneCountInString(prefix)+loc.Column-1))
		}
	}

	return redactPasswords(b.String())
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
	ExitCode   int    `json:"exitCode"`

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

// failedFilePattern extracts the file path from the scaffolded templates'
// per-file failure attribution: RAISE EXCEPTION 'Failed in %: %', path, err.
var failedFilePattern = regexp.MustCompile(`Failed in (\S+\.sql)`)

// NewErrorDetail extracts structured diagnostics from an error chain.
// Returns nil for a nil error.
func NewErrorDetail(err error) *ErrorDetail {
	if err == nil {
		return nil
	}
	d := &ErrorDetail{
		Message:  redactPasswords(err.Error()),
		ExitCode: ExitCodeForError(err),
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		d.SQLState = pgErr.Code
		d.Detail = redactPasswords(pgErr.Detail)
		d.Hint = redactPasswords(pgErr.Hint)
		d.Where = redactPasswords(pgErr.Where)
		if m := failedFilePattern.FindStringSubmatch(pgErr.Message); m != nil {
			d.FailedFile = m[1]
		}
	}
	if loc := LocateError(err); loc != nil {
		d.Script = loc.Script
		d.Line = loc.Line
		d.Column = loc.Column
		d.SourceLine = redactPasswords(loc.SourceLine)
		d.ScriptExpanded = loc.Expanded
	}
	return d
}

// passwordKVPattern matches the keyword form: `password=secret`, `sslpassword=x`,
// and the quoted values splitADONETPairs accepts (`Password='p ass'`). The
// unquoted value ends at whitespace, `&` or `;` — the separators of the URI
// query and ADO.NET forms respectively.
var passwordKVPattern = regexp.MustCompile(`(?i)password\s*=\s*('[^']*'|"[^"]*"|[^\s&;'"]*)`)

// passwordURIPattern matches `scheme://user:password@host`. The password runs to
// the LAST `@` in the token, not the first: an unescaped `@` inside a password
// is exactly what makes url.Parse fail, and its error embeds the raw string, so
// the first-`@` reading published everything after it verbatim. Go's own
// parseAuthority splits on the last `@` too, so this agrees with the parser
// whose failure produced the message.
var passwordURIPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://[^:/@\s]+):[^\s]*@`)

// redactPasswords replaces password-looking substrings with `[redacted]`.
// Handles the libpq keyword form (`password=secret`), the ADO.NET quoted form
// (`Password='p ass'`), and the URI form (`postgresql://user:secret@host/db`).
//
// The keyword pass runs first so a `sslpassword=` inside a URI query string is
// consumed before the greedy URI pass could anchor on an `@` within it.
func redactPasswords(s string) string {
	s = passwordKVPattern.ReplaceAllString(s, "password=[redacted]")
	s = passwordURIPattern.ReplaceAllString(s, "$1:[redacted]@")
	return s
}
