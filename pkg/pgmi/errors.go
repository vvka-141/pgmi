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

	// --timeout expiry surfaces as context.DeadlineExceeded; distinguish it
	// from a generic failure so operators can tell "timed out" from "SQL failed".
	if errors.Is(err, context.DeadlineExceeded) {
		return ExitTimeout
	}

	// Check for sentinel errors
	switch {
	case errors.Is(err, ErrConcurrentDeploy):
		return ExitConcurrentDeploy
	case errors.Is(err, ErrInvalidConfig):
		return ExitConfigError
	case errors.Is(err, ErrDeploySQLNotFound):
		return ExitDeploySQLMissing
	case errors.Is(err, ErrApprovalDenied):
		return ExitApprovalDenied
	case errors.Is(err, ErrExecutionFailed):
		return ExitExecutionFailed
	case errors.Is(err, ErrConnectionFailed):
		return ExitConnectionError
	case errors.Is(err, ErrUnsupportedAuthMethod):
		return ExitConfigError
	}

	errStr := err.Error()

	// Check for Cobra usage errors
	if strings.Contains(errStr, "unknown flag") ||
		strings.Contains(errStr, "unknown shorthand flag") ||
		strings.Contains(errStr, "accepts ") ||
		strings.Contains(errStr, "required flag") ||
		strings.Contains(errStr, "invalid argument") ||
		strings.Contains(errStr, "missing required argument") {
		return ExitUsageError
	}

	// pgmi's own usage errors: a bad template name or a non-empty init target
	// are invalid invocations, not runtime failures — exit 2 like cobra usage
	// errors, per the exit-code table.
	if strings.Contains(errStr, "unknown template") ||
		strings.Contains(errStr, "is not empty") {
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

// passwordKVPattern matches libpq-style `password=<value>` and key=value
// connection string fragments. Terminates at whitespace, ampersand, or end.
var passwordKVPattern = regexp.MustCompile(`(?i)password=[^\s&'"]*`)

// passwordURIPattern matches `user:password@host` inside URI connection strings.
// The password group is everything between : and @, non-greedy.
var passwordURIPattern = regexp.MustCompile(`(://[^:/@\s]+):[^@\s]*@`)

// redactPasswords replaces password-looking substrings with `[redacted]`.
// Handles both libpq keyword form (`password=secret`) and URI form
// (`postgresql://user:secret@host/db`).
func redactPasswords(s string) string {
	s = passwordKVPattern.ReplaceAllString(s, "password=[redacted]")
	s = passwordURIPattern.ReplaceAllString(s, "$1:[redacted]@")
	return s
}
