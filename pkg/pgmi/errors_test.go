package pgmi_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestExitCodeForError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		// nil
		{"nil error", nil, pgmi.ExitSuccess},

		// sentinel errors
		{"ErrInvalidConfig", pgmi.ErrInvalidConfig, pgmi.ExitConfigError},
		{"ErrDeploySQLNotFound", pgmi.ErrDeploySQLNotFound, pgmi.ExitDeploySQLMissing},
		{"ErrApprovalDenied", pgmi.ErrApprovalDenied, pgmi.ExitApprovalDenied},
		{"ErrExecutionFailed", pgmi.ErrExecutionFailed, pgmi.ExitExecutionFailed},
		{"ErrConnectionFailed", pgmi.ErrConnectionFailed, pgmi.ExitConnectionError},
		{"ErrUnsupportedAuthMethod", pgmi.ErrUnsupportedAuthMethod, pgmi.ExitConfigError},

		// wrapped sentinel errors
		{"wrapped ErrInvalidConfig", fmt.Errorf("config problem: %w", pgmi.ErrInvalidConfig), pgmi.ExitConfigError},
		{"wrapped ErrDeploySQLNotFound", fmt.Errorf("missing: %w", pgmi.ErrDeploySQLNotFound), pgmi.ExitDeploySQLMissing},
		{"wrapped ErrApprovalDenied", fmt.Errorf("user said no: %w", pgmi.ErrApprovalDenied), pgmi.ExitApprovalDenied},
		{"wrapped ErrExecutionFailed", fmt.Errorf("sql broke: %w", pgmi.ErrExecutionFailed), pgmi.ExitExecutionFailed},
		{"wrapped ErrConnectionFailed", fmt.Errorf("db down: %w", pgmi.ErrConnectionFailed), pgmi.ExitConnectionError},
		{"double wrapped ErrExecutionFailed", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", pgmi.ErrExecutionFailed)), pgmi.ExitExecutionFailed},

		// joined errors (errors.Join)
		{"joined ErrExecutionFailed", errors.Join(pgmi.ErrExecutionFailed, errors.New("pg error")), pgmi.ExitExecutionFailed},

		// cobra usage errors (string patterns)
		{"unknown flag", errors.New("unknown flag --foo"), pgmi.ExitUsageError},
		{"unknown shorthand flag", errors.New("unknown shorthand flag: 'x'"), pgmi.ExitUsageError},
		{"accepts args", errors.New("accepts 1 arg(s), received 0"), pgmi.ExitUsageError},
		{"required flag", errors.New("required flag \"database\" not set"), pgmi.ExitUsageError},
		{"invalid argument", errors.New("invalid argument \"abc\" for \"--port\""), pgmi.ExitUsageError},

		// connection error string patterns
		{"failed to connect", errors.New("failed to connect to host"), pgmi.ExitConnectionError},
		{"connection refused", errors.New("dial tcp: connection refused"), pgmi.ExitConnectionError},
		{"no such host", errors.New("lookup db.example.com: no such host"), pgmi.ExitConnectionError},

		// general error
		{"unclassified error", errors.New("something unexpected"), pgmi.ExitGeneralError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pgmi.ExitCodeForError(tt.err)
			if got != tt.want {
				t.Errorf("ExitCodeForError(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestFormatError_Nil(t *testing.T) {
	if got := pgmi.FormatError(nil); got != "" {
		t.Errorf("FormatError(nil) = %q, want empty string", got)
	}
}

func TestFormatError_PlainError(t *testing.T) {
	err := errors.New("something broke")
	got := pgmi.FormatError(err)
	if got != "something broke" {
		t.Errorf("FormatError = %q, want %q", got, "something broke")
	}
}

func TestFormatError_PgErrorWithAllFields(t *testing.T) {
	pgErr := &pgconn.PgError{
		Severity: "ERROR",
		Code:     "23505",
		Message:  "duplicate key value violates unique constraint \"users_email_key\"",
		Detail:   "Key (email)=(alice@example.com) already exists.",
		Hint:     "Use UPDATE instead of INSERT, or ON CONFLICT.",
		Where:    "PL/pgSQL function insert_user(text) line 5 at SQL statement",
	}

	got := pgmi.FormatError(pgErr)

	wantSubstrings := []string{
		"duplicate key value",
		"DETAIL: Key (email)=(alice@example.com) already exists.",
		"HINT: Use UPDATE instead of INSERT, or ON CONFLICT.",
		"WHERE: PL/pgSQL function insert_user(text) line 5 at SQL statement",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("FormatError missing %q\ngot: %s", want, got)
		}
	}
}

func TestFormatError_WrappedPgError(t *testing.T) {
	pgErr := &pgconn.PgError{
		Message: "relation \"missing_table\" does not exist",
		Hint:    "Perhaps you meant existing_table.",
	}
	wrapped := fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, pgErr)

	got := pgmi.FormatError(wrapped)

	if !strings.Contains(got, "relation \"missing_table\" does not exist") {
		t.Errorf("FormatError missing pg message, got: %s", got)
	}
	if !strings.Contains(got, "HINT: Perhaps you meant existing_table.") {
		t.Errorf("FormatError missing HINT line, got: %s", got)
	}
}

func TestFormatError_PgErrorEmptyFieldsOmitted(t *testing.T) {
	pgErr := &pgconn.PgError{
		Message: "column \"foo\" does not exist",
	}
	got := pgmi.FormatError(pgErr)

	if strings.Contains(got, "DETAIL:") || strings.Contains(got, "HINT:") || strings.Contains(got, "WHERE:") {
		t.Errorf("FormatError added empty diagnostic fields, got: %s", got)
	}
}
