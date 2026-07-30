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

		// ErrConnectionFailed wrapping DeadlineExceeded: sentinel wins over deadline
		{"connection timeout (dial deadline)", fmt.Errorf("connect: %w", fmt.Errorf("dial: %w", errors.Join(pgmi.ErrConnectionFailed, context.DeadlineExceeded))), pgmi.ExitConnectionError},

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

		// usage errors (ErrUsage sentinel)
		{"unknown flag", fmt.Errorf("%w: unknown flag --foo", pgmi.ErrUsage), pgmi.ExitUsageError},
		{"unknown shorthand flag", fmt.Errorf("%w: unknown shorthand flag: 'x'", pgmi.ErrUsage), pgmi.ExitUsageError},
		{"accepts args", fmt.Errorf("%w: accepts 1 arg(s), received 0", pgmi.ErrUsage), pgmi.ExitUsageError},
		{"required flag", fmt.Errorf("%w: required flag \"database\" not set", pgmi.ErrUsage), pgmi.ExitUsageError},
		{"invalid argument", fmt.Errorf("%w: invalid argument \"abc\" for \"--port\"", pgmi.ErrUsage), pgmi.ExitUsageError},
		{"unknown template", fmt.Errorf("%w: unknown template \"foo\" (available: [basic advanced])", pgmi.ErrUsage), pgmi.ExitUsageError},
		{"non-empty init dir", fmt.Errorf("%w: target directory 'x' is not empty", pgmi.ErrUsage), pgmi.ExitUsageError},
		{"wrapped ErrUsage", fmt.Errorf("outer: %w", fmt.Errorf("%w: bad args", pgmi.ErrUsage)), pgmi.ExitUsageError},
		{"bare ErrUsage", pgmi.ErrUsage, pgmi.ExitUsageError},

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

func TestLocateError(t *testing.T) {
	const script = "SELECT 1;\nSELECT 2;\nSELEC 3;\n"

	// "SELEC" starts at line 3; the script is 20 characters before it.
	pgErr := &pgconn.PgError{
		Code:     "42601",
		Message:  `syntax error at or near "SELEC"`,
		Position: 21,
	}

	tests := []struct {
		name string
		err  error
		want *pgmi.SQLLocation
	}{
		{
			name: "nil error",
			err:  nil,
			want: nil,
		},
		{
			name: "pg error without a script attached cannot be located",
			err:  fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, pgErr),
			want: nil,
		},
		{
			name: "script attached but no position (most runtime errors)",
			err: pgmi.NewScriptError(
				&pgconn.PgError{Code: "23505", Message: "duplicate key"},
				"deploy.sql", script, false,
			),
			want: nil,
		},
		{
			name: "position resolves to line, column, and the offending line",
			err:  pgmi.NewScriptError(pgErr, "deploy.sql", script, false),
			want: &pgmi.SQLLocation{
				Script: "deploy.sql", Line: 3, Column: 1, SourceLine: "SELEC 3;", Expanded: false,
			},
		},
		{
			name: "expanded script is flagged so the line is not mistaken for the file on disk",
			err: fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed,
				pgmi.NewScriptError(pgErr, "deploy.sql", script, true)),
			want: &pgmi.SQLLocation{
				Script: "deploy.sql", Line: 3, Column: 1, SourceLine: "SELEC 3;", Expanded: true,
			},
		},
		{
			name: "position past the end of the script is rejected, not guessed",
			err: pgmi.NewScriptError(
				&pgconn.PgError{Code: "42601", Position: 9999}, "deploy.sql", script, false,
			),
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pgmi.LocateError(tt.err)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected no location, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected location %+v, got nil", tt.want)
			}
			if *got != *tt.want {
				t.Errorf("location mismatch\ngot:  %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

// PostgreSQL reports Position in characters, not bytes. A byte-indexed walk
// would drift past every multi-byte character earlier in the script.
func TestLocateError_MultibyteCharactersDoNotSkewTheLine(t *testing.T) {
	script := "SELECT 'héllo wörld — ünicode';\nSELEC 2;\n"

	// Line 1 is 31 characters plus the newline, so line 2 begins at character 33.
	// Counted in bytes it would be 39 — the drift this test exists to catch.
	err := pgmi.NewScriptError(
		&pgconn.PgError{Code: "42601", Position: 33}, "deploy.sql", script, false,
	)

	loc := pgmi.LocateError(err)
	if loc == nil {
		t.Fatal("expected a location")
	}
	if loc.Line != 2 || loc.Column != 1 {
		t.Errorf("expected line 2, column 1; got line %d, column %d", loc.Line, loc.Column)
	}
	if loc.SourceLine != "SELEC 2;" {
		t.Errorf("expected source line %q, got %q", "SELEC 2;", loc.SourceLine)
	}
}

func TestFormatError_IncludesLocationAndPointsAtTheOffendingLine(t *testing.T) {
	err := pgmi.NewScriptError(
		&pgconn.PgError{
			Code:     "42601",
			Message:  `syntax error at or near "SELEC"`,
			Position: 11,
		},
		"deploy.sql", "SELECT 1;\nSELEC 2;\n", true,
	)

	out := pgmi.FormatError(err)

	for _, want := range []string{
		"LOCATION: deploy.sql line 2, column 1",
		"pgmi_test() macros shift line numbers",
		"LINE 2: SELEC 2;",
		"^",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatError output missing %q\ngot:\n%s", want, out)
		}
	}
}

func TestNewErrorDetail_CarriesLocationForJSON(t *testing.T) {
	err := pgmi.NewScriptError(
		&pgconn.PgError{Code: "42601", Message: "syntax error", Position: 11},
		"deploy.sql", "SELECT 1;\nSELEC 2;\n", true,
	)

	d := pgmi.NewErrorDetail(err)
	if d.Line != 2 || d.Column != 1 {
		t.Errorf("expected line 2 column 1, got line %d column %d", d.Line, d.Column)
	}
	if d.Script != "deploy.sql" || d.SourceLine != "SELEC 2;" || !d.ScriptExpanded {
		t.Errorf("unexpected detail: %+v", d)
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

	t.Run("test failure surfaces failedFile from pgmi_test_generate attribution", func(t *testing.T) {
		pgErr := &pgconn.PgError{
			Code:    "P0001",
			Message: "Failed in ./__test__/test_user_crud.sql: user count assertion failed: expected 999, got 5",
			Detail:  "",
			Where:   "PL/pgSQL function inline_code_block line 3 at RAISE",
		}
		d := pgmi.NewErrorDetail(fmt.Errorf("execution failed: %w", pgErr))
		if d.FailedFile != "./__test__/test_user_crud.sql" {
			t.Errorf("FailedFile = %q, want ./__test__/test_user_crud.sql", d.FailedFile)
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

// TestFormatError_RedactsAwkwardPasswordForms covers the two forms the previous
// patterns published verbatim: an unescaped '@' inside a URI password (which is
// precisely what makes url.Parse fail, and its error embeds the raw string), and
// the quoted ADO.NET values splitADONETPairs accepts but the redactor's
// character class excluded.
func TestFormatError_RedactsAwkwardPasswordForms(t *testing.T) {
	tests := []struct {
		name string
		text string
		// Every fragment that must not survive. For a password holding an '@',
		// the whole value disappearing is not enough: stopping at the first '@'
		// left the tail on stdout, which is the leak this ticket reports.
		secrets []string
	}{
		{
			name:    "@ inside a URI password",
			text:    `parse "postgresql://u:p@ss@db/app": net/url: invalid userinfo`,
			secrets: []string{"p@ss", "ss@db"},
		},
		{
			name:    "@ and more after it",
			text:    `failed to connect to postgresql://admin:p@ssw0rd@db.example.com:5432/prod: timeout`,
			secrets: []string{"p@ssw0rd", "ssw0rd"},
		},
		{
			name:    "single-quoted ADO.NET value",
			text:    `invalid connection string: Password='p ass';Host=h`,
			secrets: []string{"p ass"},
		},
		{
			name:    "double-quoted ADO.NET value",
			text:    `invalid connection string: Password="x y";Host=h`,
			secrets: []string{"x y"},
		},
		{
			name:    "sslpassword keyword",
			text:    `could not read key: sslpassword=abc123 sslmode=verify-full`,
			secrets: []string{"abc123"},
		},
		{
			name:    "spaces around the keyword separator",
			text:    `bad dsn: Password = 'sp aced' ;Host=h`,
			secrets: []string{"sp aced"},
		},
		{
			name:    "sslpassword with an @ inside a URI query",
			text:    `parse "postgresql://u:pw@h/db?sslpassword=a@b": bad`,
			secrets: []string{"a@b", "a@", "@b"},
		},
		// On the Azure and AWS paths the password IS a bearer token, and every
		// case above uses a short simple word. A stricter character class in
		// passwordURIPattern would keep passing all of them while publishing a
		// live credential, so the two real token shapes are pinned here.
		{
			// Azure/GCP: a JWT. Dots and underscores are the risk.
			name: "URI password is a JWT",
			text: `parse "postgresql://u:eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c@h/db": bad`,
			secrets: []string{
				"SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
				"eyJhbGciOiJIUzI1NiJ9",
			},
		},
		{
			// AWS RDS IAM: a presigned URL, so the password itself carries
			// '?', '&' and '=' — the separators of the other two forms.
			name:    "URI password is an RDS IAM presigned URL",
			text:    `parse "postgresql://app:db.abc.us-east-1.rds.amazonaws.com%3A5432%2F%3FAction%3Dconnect%26X-Amz-Signature%3Dsigsecret123@h/db": bad`,
			secrets: []string{"sigsecret123", "X-Amz-Signature"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatted := pgmi.FormatError(errors.New(tt.text))
			d := pgmi.NewErrorDetail(errors.New(tt.text))
			if d == nil {
				t.Fatal("NewErrorDetail returned nil for a non-nil error")
			}

			for _, secret := range tt.secrets {
				if strings.Contains(formatted, secret) {
					t.Errorf("FormatError leaked %q:\n%s", secret, formatted)
				}
				if strings.Contains(d.Message, secret) {
					t.Errorf("ErrorDetail.Message leaked %q:\n%s", secret, d.Message)
				}
			}
			if !strings.Contains(d.Message, "[redacted]") {
				t.Errorf("nothing was redacted, so the message was not recognised as carrying a secret:\n%s",
					d.Message)
			}
		})
	}
}

// Redaction must not eat the diagnostics around it. Over-redacting a whole error
// message hides the host and the reason, which is how a secure-but-useless
// failure message happens.
func TestFormatError_RedactionKeepsSurroundingContext(t *testing.T) {
	formatted := pgmi.FormatError(errors.New(
		`failed to connect to postgresql://admin:p@ssw0rd@db.example.com:5432/prod: connection refused`))

	for _, want := range []string{"db.example.com:5432/prod", "connection refused", "admin", "[redacted]"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("redaction removed %q from the message:\n%s", want, formatted)
		}
	}
}

// The table above covers context.DeadlineExceeded plainly wrapped. A real
// --timeout expiry does not arrive that way: the deploy is inside deploy.SQL
// execution, so the deadline reaches ExitCodeForError wrapped in
// ErrExecutionFailed. That chain returned 13, telling CI the script was broken
// when it had merely run out of time — while the plain case above returned 16
// and the suite stayed green.
//
// Confirmed against PG 17.10 with a deploy.sql of `SELECT pg_sleep(30)` under
// --timeout 5s: exit 13 before, 16 after.
func TestExitCodeForError_TimeoutOutranksExecutionFailure(t *testing.T) {
	timedOutDeploy := fmt.Errorf("%w: timeout: %w", pgmi.ErrExecutionFailed, context.DeadlineExceeded)

	if got := pgmi.ExitCodeForError(timedOutDeploy); got != pgmi.ExitTimeout {
		t.Errorf("exit code %d, want %d: a deploy killed by --timeout is a timeout, "+
			"not a broken script", got, pgmi.ExitTimeout)
	}
}

// PostgreSQL's own statement_timeout is a SQL failure and keeps 13. It arrives
// as SQLSTATE 57014 with no Go deadline in the chain, which is what keeps the
// two kinds of timeout distinguishable.
func TestExitCodeForError_StatementTimeoutStaysExecutionFailure(t *testing.T) {
	pgTimeout := fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed,
		&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"})

	if got := pgmi.ExitCodeForError(pgTimeout); got != pgmi.ExitExecutionFailed {
		t.Errorf("exit code %d, want %d: statement_timeout is the server rejecting the "+
			"statement, not pgmi's deadline", got, pgmi.ExitExecutionFailed)
	}
}

// A connect timeout must keep 11. pgx's dial timeout wraps DeadlineExceeded, so
// this is the case the sentinel ordering exists to protect, and moving the
// deadline check earlier must not disturb it.
func TestExitCodeForError_ConnectTimeoutStaysConnectionError(t *testing.T) {
	connTimeout := fmt.Errorf("%w: %w", pgmi.ErrConnectionFailed, context.DeadlineExceeded)

	if got := pgmi.ExitCodeForError(connTimeout); got != pgmi.ExitConnectionError {
		t.Errorf("exit code %d, want %d: a connect timeout is a connection failure",
			got, pgmi.ExitConnectionError)
	}
}
