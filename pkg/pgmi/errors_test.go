package pgmi_test

import (
	"context"
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
		{"ErrConcurrentDeploy", pgmi.ErrConcurrentDeploy, pgmi.ExitConcurrentDeploy},

		// SIGINT / Ctrl-C
		{"context.Canceled", context.Canceled, pgmi.ExitInterrupted},
		{"wrapped context.Canceled", fmt.Errorf("aborted: %w", context.Canceled), pgmi.ExitInterrupted},

		// --timeout expiry
		{"context.DeadlineExceeded", context.DeadlineExceeded, pgmi.ExitTimeout},
		{"wrapped context.DeadlineExceeded", fmt.Errorf("deploy: %w", context.DeadlineExceeded), pgmi.ExitTimeout},

		// wrapped sentinel errors
		{"wrapped ErrInvalidConfig", fmt.Errorf("config problem: %w", pgmi.ErrInvalidConfig), pgmi.ExitConfigError},
		{"wrapped ErrDeploySQLNotFound", fmt.Errorf("missing: %w", pgmi.ErrDeploySQLNotFound), pgmi.ExitDeploySQLMissing},
		{"wrapped ErrApprovalDenied", fmt.Errorf("user said no: %w", pgmi.ErrApprovalDenied), pgmi.ExitApprovalDenied},
		{"wrapped ErrExecutionFailed", fmt.Errorf("sql broke: %w", pgmi.ErrExecutionFailed), pgmi.ExitExecutionFailed},
		{"wrapped ErrConnectionFailed", fmt.Errorf("db down: %w", pgmi.ErrConnectionFailed), pgmi.ExitConnectionError},
		{"wrapped ErrConcurrentDeploy", fmt.Errorf("hit lock: %w", pgmi.ErrConcurrentDeploy), pgmi.ExitConcurrentDeploy},
		{"double wrapped ErrExecutionFailed", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", pgmi.ErrExecutionFailed)), pgmi.ExitExecutionFailed},

		// joined errors (errors.Join)
		{"joined ErrExecutionFailed", errors.Join(pgmi.ErrExecutionFailed, errors.New("pg error")), pgmi.ExitExecutionFailed},

		// cobra usage errors (string patterns)
		{"unknown flag", errors.New("unknown flag --foo"), pgmi.ExitUsageError},
		{"unknown shorthand flag", errors.New("unknown shorthand flag: 'x'"), pgmi.ExitUsageError},
		{"accepts args", errors.New("accepts 1 arg(s), received 0"), pgmi.ExitUsageError},
		{"required flag", errors.New("required flag \"database\" not set"), pgmi.ExitUsageError},
		{"invalid argument", errors.New("invalid argument \"abc\" for \"--port\""), pgmi.ExitUsageError},

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

func TestNewErrorDetail(t *testing.T) {
	t.Run("nil error returns nil", func(t *testing.T) {
		if d := pgmi.NewErrorDetail(nil); d != nil {
			t.Fatalf("expected nil, got %+v", d)
		}
	})

	t.Run("plain error carries message and exit code", func(t *testing.T) {
		d := pgmi.NewErrorDetail(fmt.Errorf("boom: %w", pgmi.ErrConnectionFailed))
		if d.Message != "boom: connection failed" {
			t.Errorf("Message = %q", d.Message)
		}
		if d.ExitCode != pgmi.ExitConnectionError {
			t.Errorf("ExitCode = %d, want %d", d.ExitCode, pgmi.ExitConnectionError)
		}
		if d.SQLState != "" {
			t.Errorf("SQLState = %q, want empty", d.SQLState)
		}
	})

	t.Run("pg error surfaces diagnostics and failed file", func(t *testing.T) {
		pgErr := &pgconn.PgError{
			Code:    "P0001",
			Message: "Failed in ./migrations/003_broken.sql: syntax error",
			Detail:  "some detail",
			Hint:    "some hint",
			Where:   "PL/pgSQL function inline_code_block line 12 at RAISE",
		}
		d := pgmi.NewErrorDetail(fmt.Errorf("execution failed: %w", pgErr))
		if d.SQLState != "P0001" {
			t.Errorf("SQLState = %q", d.SQLState)
		}
		if d.Detail != "some detail" || d.Hint != "some hint" {
			t.Errorf("Detail/Hint = %q/%q", d.Detail, d.Hint)
		}
		if !strings.Contains(d.Where, "line 12") {
			t.Errorf("Where = %q", d.Where)
		}
		if d.FailedFile != "./migrations/003_broken.sql" {
			t.Errorf("FailedFile = %q", d.FailedFile)
		}
	})

	t.Run("passwords are redacted in all fields", func(t *testing.T) {
		pgErr := &pgconn.PgError{
			Code:   "28P01",
			Detail: "tried postgresql://admin:hunter2@db/prod",
		}
		d := pgmi.NewErrorDetail(fmt.Errorf("connect password=hunter2 failed: %w", pgErr))
		if strings.Contains(d.Message, "hunter2") || strings.Contains(d.Detail, "hunter2") {
			t.Errorf("password leaked: Message=%q Detail=%q", d.Message, d.Detail)
		}
	})
}
