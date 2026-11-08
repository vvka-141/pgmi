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
			name: "no position, no DO block: deploy.sql without a line",
			err: newScriptError(
				&pgconn.PgError{Code: "23505", Message: "duplicate key"},
				"deploy.sql", script, false,
			),
			want: &pgmi.SQLLocation{Script: "deploy.sql"},
		},
		{
			name: "position resolves to line, column, and the offending line",
			err:  newScriptError(pgErr, "deploy.sql", script, false),
			want: &pgmi.SQLLocation{
				Script: "deploy.sql", Line: 3, Column: 1, SourceLine: "SELEC 3;", Expanded: false,
			},
		},
		{
			name: "expanded script is flagged so the line is not mistaken for the file on disk",
			err: fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed,
				newScriptError(pgErr, "deploy.sql", script, true)),
			want: &pgmi.SQLLocation{
				Script: "deploy.sql", Line: 3, Column: 1, SourceLine: "SELEC 3;", Expanded: true,
			},
		},
		{
			name: "position past the end of the script is rejected, not guessed",
			err: newScriptError(
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
	err := newScriptError(
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

// An error inside an EXECUTEd file is attributed by the file's text, not by
// how deploy.sql words its re-raise (PGMI-376). The PgError shapes are what
// PostgreSQL 17 returns for EXECUTE: a parse error carries InternalQuery and
// InternalPosition, a runtime error only the `SQL statement "..."` context.
func TestFailedFile_AttributedByExecutedText(t *testing.T) {
	migration := "CREATE TABLE a(x int);\n\n\nSELEC 1;\n"
	files := []pgmi.FileMetadata{
		{Path: "./migrations/001.sql", Content: "CREATE TABLE z(x int);\n"},
		{Path: "./migrations/002.sql", Content: migration},
	}
	attach := func(pgErr *pgconn.PgError) error {
		err := newScriptError(pgErr, "deploy.sql", "DO $$ ... $$;", false)
		err.(*pgmi.ScriptError).Files = files
		return fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, err)
	}

	t.Run("parse error resolves to a line in the file", func(t *testing.T) {
		err := attach(&pgconn.PgError{
			Code: "42601", Message: `syntax error at or near "SELEC"`,
			InternalQuery: migration, InternalPosition: 26,
			Where: "PL/pgSQL function inline_code_block line 1 at EXECUTE",
		})
		d := pgmi.NewErrorDetail(err)
		if d.FailedFile != "./migrations/002.sql" || d.Script != "./migrations/002.sql" || d.Line != 4 || d.Column != 1 {
			t.Fatalf("want ./migrations/002.sql line 4 column 1, got %+v", d)
		}
		if out := pgmi.FormatError(err); !strings.Contains(out, "LOCATION: ./migrations/002.sql line 4, column 1") {
			t.Errorf("FormatError does not locate the file line:\n%s", out)
		}
	})

	t.Run("runtime error names the file from the statement context", func(t *testing.T) {
		err := attach(&pgconn.PgError{
			Code: "22012", Message: "division by zero",
			Where: "SQL statement \"" + migration + "\"\nPL/pgSQL function inline_code_block line 1 at EXECUTE",
		})
		if got := pgmi.FailedFile(err); got != "./migrations/002.sql" {
			t.Fatalf("FailedFile = %q", got)
		}
		if out := pgmi.FormatError(err); !strings.Contains(out, "LOCATION: ./migrations/002.sql") {
			t.Errorf("FormatError does not name the file:\n%s", out)
		}
	})

	t.Run("human output names the file instead of quoting it", func(t *testing.T) {
		where := "SQL statement \"" + migration + "\"\nPL/pgSQL function inline_code_block line 1 at EXECUTE"
		err := attach(&pgconn.PgError{Code: "22012", Message: "division by zero", Where: where})
		out := pgmi.FormatError(err)
		if strings.Contains(out, "SELEC 1;") || !strings.Contains(out, "WHERE: SQL statement in ./migrations/002.sql\n") {
			t.Errorf("WHERE must name the file rather than print its text:\n%s", out)
		}
		if d := pgmi.NewErrorDetail(err); d.Where != "SQL statement in ./migrations/002.sql\nPL/pgSQL function inline_code_block line 1 at EXECUTE" {
			t.Errorf("--json names the file instead of quoting it, got %q", d.Where)
		}
	})

	t.Run("message convention still works without matching text", func(t *testing.T) {
		err := attach(&pgconn.PgError{Code: "P0001", Message: "Failed in ./x.sql: boom"})
		if got := pgmi.FailedFile(err); got != "./x.sql" {
			t.Fatalf("FailedFile = %q", got)
		}
	})
}

func TestFormatError_IncludesLocationAndPointsAtTheOffendingLine(t *testing.T) {
	err := newScriptError(
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
	err := newScriptError(
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
		if d.Message != "boom" {
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
		{
			name:    "ADO.NET Pwd keyword",
			text:    `invalid connection string: Host=h;Pwd=s3cret;Database=x`,
			secrets: []string{"s3cret"},
		},
		{
			name:    "PWD with spaces around the separator",
			text:    `invalid connection string: Host=h;PWD = s3cret;Database=x`,
			secrets: []string{"s3cret"},
		},
		{
			name:    "ADO.NET doubled quote inside the value",
			text:    `invalid connection string: Password='it''s-secret';Host=h`,
			secrets: []string{"it", "s-secret"},
		},
		{
			name:    "libpq backslash-escaped quote inside the value",
			text:    `cannot parse: password='a\'bsecret' host=h`,
			secrets: []string{"bsecret"},
		},
		{
			name:    "double-quoted value with a doubled quote",
			text:    `invalid connection string: Password="x""ysecret";Host=h`,
			secrets: []string{"ysecret"},
		},
		{
			name:    "SQL PASSWORD literal in a statement context",
			text:    `SQL statement "ALTER ROLE app_admin WITH PASSWORD 'TopSecret 123' CONNECTION LIMIT 10"`,
			secrets: []string{"TopSecret 123", "TopSecret"},
		},
		{
			name:    "SQL PASSWORD literal with a doubled quote",
			text:    `SQL statement "CREATE ROLE a LOGIN password 'it''s secret' CONNECTION LIMIT 10"`,
			secrets: []string{"it''s secret", "s secret"},
		},
		{
			name:    "SQL PASSWORD as the E-string format('%L') writes for a backslash",
			text:    `SQL statement "ALTER ROLE a WITH PASSWORD E'back\\slash '' quote' CONNECTION LIMIT 10"`,
			secrets: []string{"back", "slash", "quote'"},
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
		{
			// format('password=%L', v) writes an E'' string when the value
			// holds a backslash; a quoted value is redacted whole, spaces and all.
			name:    "keyword form, E'' quoted value with spaces",
			text:    `NOTICE: password=E's3\\cret # not a comment'`,
			secrets: []string{"s3", "not a comment"},
		},
		{
			name:    "named argument, quoted value with spaces",
			text:    `CONTEXT: SELECT connect(password => 's3cret # not a comment')`,
			secrets: []string{"s3cret", "not a comment"},
		},
		{
			name:    "JSON member, quoted value with spaces",
			text:    `NOTICE: {"user": "app", "password": "s3cret # not a comment"}`,
			secrets: []string{"s3cret", "not a comment"},
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

// pgmi stops a deploy whose context ended with a cancel request, so the server
// answers 57014 as it does for statement_timeout. The context in the chain is
// what tells them apart.
func TestExitCodeForError_CancelledStatementFollowsTheContext(t *testing.T) {
	cancelled := fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed,
		&pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"})
	for _, tc := range []struct {
		cause error
		want  int
	}{
		{context.DeadlineExceeded, pgmi.ExitTimeout},
		{context.Canceled, pgmi.ExitInterrupted},
	} {
		if got := pgmi.ExitCodeForError(errors.Join(cancelled, tc.cause)); got != tc.want {
			t.Errorf("57014 with %v: exit code %d, want %d", tc.cause, got, tc.want)
		}
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

// An editor autosave (#task.sql#) is loaded but never runs; attributing to it
// sent an agent to edit a file that does not matter. Two SQL files with the
// same text cannot be told apart, so both are named.
func TestFailedFile_OnlySQLFilesAndEveryIdenticalCandidate(t *testing.T) {
	content := "SELEC broken;\n"
	parseErr := func() *pgconn.PgError {
		return &pgconn.PgError{
			Code: "42601", Message: `syntax error at or near "SELEC"`,
			InternalQuery: content, InternalPosition: 1,
			Where: "PL/pgSQL function inline_code_block line 1 at EXECUTE",
		}
	}
	attach := func(files []pgmi.FileMetadata, pgErr *pgconn.PgError) error {
		err := newScriptError(pgErr, "deploy.sql", "DO $$ ... $$;", false)
		err.(*pgmi.ScriptError).Files = files
		return fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, err)
	}

	t.Run("a non-SQL file with the same text is not blamed", func(t *testing.T) {
		err := attach([]pgmi.FileMetadata{
			{Path: "./semantic/#task.sql#", Extension: ".sql#", Content: content},
			{Path: "./semantic/task.sql", Extension: ".sql", Content: content},
		}, parseErr())
		d := pgmi.NewErrorDetail(err)
		if d.FailedFile != "./semantic/task.sql" || d.Script != "./semantic/task.sql" {
			t.Fatalf("want ./semantic/task.sql, got failedFile=%q script=%q", d.FailedFile, d.Script)
		}
		if len(d.FailedFileCandidates) != 0 {
			t.Errorf("one SQL file matched, yet candidates = %v", d.FailedFileCandidates)
		}
		if strings.Contains(pgmi.FormatError(err), "#task.sql#") {
			t.Errorf("FormatError names the autosave:\n%s", pgmi.FormatError(err))
		}
	})

	t.Run("two SQL files with the same text are both named", func(t *testing.T) {
		err := attach([]pgmi.FileMetadata{
			{Path: "./semantic/task.sql", Extension: ".sql", Content: content},
			{Path: "./semantic/task copy.sql", Extension: ".sql", Content: content},
		}, parseErr())
		d := pgmi.NewErrorDetail(err)
		want := []string{"./semantic/task.sql", "./semantic/task copy.sql"}
		if fmt.Sprint(d.FailedFileCandidates) != fmt.Sprint(want) {
			t.Fatalf("candidates = %v, want %v", d.FailedFileCandidates, want)
		}
		if out := pgmi.FormatError(err); !strings.Contains(out, "./semantic/task copy.sql") {
			t.Errorf("FormatError does not name the second candidate:\n%s", out)
		}
	})

	t.Run("a runtime error quoted in WHERE skips non-SQL files too", func(t *testing.T) {
		pgErr := &pgconn.PgError{
			Code: "P0001", Message: "boom",
			Where: `PL/pgSQL function inline_code_block line 1 at RAISE` + "\n" +
				`SQL statement "` + content + `"`,
		}
		err := attach([]pgmi.FileMetadata{
			{Path: "./semantic/#task.sql#", Extension: ".sql#", Content: content},
			{Path: "./semantic/task.sql", Extension: ".sql", Content: content},
		}, pgErr)
		if got := pgmi.FailedFile(err); got != "./semantic/task.sql" {
			t.Fatalf("FailedFile = %q, want ./semantic/task.sql", got)
		}
	})
}

func TestFailureRecord_ErrorInDeploySQLItself(t *testing.T) {
	head := "BEGIN;\n\nDO $$\nDECLARE\n    v int;\nBEGIN\n    v := current_setting('pgmi.owner_role');\nEND $$;\n\nCOMMIT;\n"
	err := fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, newScriptError(&pgconn.PgError{
		Code: "42704", Message: `unrecognized configuration parameter "pgmi.owner_role"`,
		Where: "PL/pgSQL function inline_code_block line 5 at assignment",
	}, "deploy.sql", head, false))
	d := pgmi.NewErrorDetail(err)
	if d.Script != "deploy.sql" || d.Line != 7 || !strings.Contains(d.SourceLine, "current_setting") || d.Phase != "deploy" {
		t.Errorf("want deploy.sql line 7, phase deploy; got script=%q line=%d source=%q phase=%q", d.Script, d.Line, d.SourceLine, d.Phase)
	}

	twoBlocks := head + "DO $$ BEGIN PERFORM 1; END $$;\n"
	err = newScriptError(&pgconn.PgError{Code: "42704", Where: "PL/pgSQL function inline_code_block line 5 at assignment"},
		"deploy.sql", twoBlocks, false)
	if d := pgmi.NewErrorDetail(err); d.Script != "deploy.sql" || d.Line != 0 {
		t.Errorf("two DO blocks: name deploy.sql, guess no line; got script=%q line=%d", d.Script, d.Line)
	}
}

func TestFailureRecord_ATestFailureIsMarkedAndLocated(t *testing.T) {
	test := "DO $$\nBEGIN\n    IF true THEN\n        RAISE EXCEPTION 'TEST FAILED: x';\n    END IF;\nEND $$;\n"
	files := []pgmi.FileMetadata{{Path: "./semantic/__test__/test_project.sql", Content: test}}
	scriptErr := newScriptError(&pgconn.PgError{
		Code: "P0001", Message: "TEST FAILED: x",
		Where: "PL/pgSQL function inline_code_block line 4 at RAISE\nSQL statement \"" + test + "\"\nPL/pgSQL function inline_code_block line 9 at EXECUTE",
	}, "deploy.sql", "DO $$ ... $$;", false)
	scriptErr.(*pgmi.ScriptError).Files = files
	d := pgmi.NewErrorDetail(fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, scriptErr))
	if d.Phase != "test" || d.Script != "./semantic/__test__/test_project.sql" || d.Line != 4 {
		t.Errorf("want phase test at the RAISE on line 4 of the test file; got phase=%q script=%q line=%d", d.Phase, d.Script, d.Line)
	}
	if strings.Contains(d.Where, "TEST FAILED: x';") {
		t.Errorf("where must name the test file, not inline it: %q", d.Where)
	}
}

func TestFormatErrorHidesSentinels(t *testing.T) {
	banned := []string{"usage error", "invalid configuration", "execution failed", "approval denied",
		"approval request failed", "overwrite workflow failed", "context canceled", "context deadline exceeded", "[", "]"}
	sentinels := []error{pgmi.ErrUsage, pgmi.ErrInvalidConfig, pgmi.ErrExecutionFailed, pgmi.ErrApprovalDenied,
		pgmi.ErrConnectionFailed, pgmi.ErrConcurrentDeploy, pgmi.ErrDeploySQLNotFound, pgmi.ErrUnsupportedAuthMethod}
	for _, s := range sentinels {
		for name, err := range map[string]error{
			"prefix":           fmt.Errorf("%w: the thing broke", s),
			"suffix":           fmt.Errorf("the thing broke: %w", s),
			"suffix then line": fmt.Errorf("%w\n(connection from PGMI_CONNECTION_STRING)", fmt.Errorf("the thing broke: %w", s)),
			"nested prefix":    fmt.Errorf("%w: %w: the thing broke", s, s),
		} {
			t.Run(s.Error()+"/"+name, func(t *testing.T) {
				got := pgmi.FormatError(err)
				if !strings.Contains(got, "the thing broke") {
					t.Errorf("message lost: %q", got)
				}
				for _, word := range []string{"usage error", "invalid configuration", "execution failed", s.Error()} {
					if strings.Contains(got, word) {
						t.Errorf("%q shows the sentinel %q", got, word)
					}
				}
				if pgmi.ExitCodeForError(err) != pgmi.ExitCodeForError(s) {
					t.Errorf("exit code changed")
				}
			})
		}
	}

	for name, tc := range map[string]struct {
		err  error
		want string
		code int
	}{
		"interrupt":         {fmt.Errorf("%w: %w", context.Canceled, fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, context.Canceled)), "interrupted", pgmi.ExitInterrupted},
		"bare interrupt":    {context.Canceled, "interrupted", pgmi.ExitInterrupted},
		"reworded":          {pgmi.WithMessage(context.Canceled, "overwrite cancelled"), "overwrite cancelled", pgmi.ExitInterrupted},
		"reworded deadline": {pgmi.WithMessage(fmt.Errorf("%w: timeout: %w", pgmi.ErrExecutionFailed, context.DeadlineExceeded), "the deploy exceeded --timeout 2s"), "the deploy exceeded --timeout 2s", pgmi.ExitCodeForError(context.DeadlineExceeded)},
	} {
		t.Run(name, func(t *testing.T) {
			got := pgmi.FormatError(tc.err)
			if got != tc.want {
				t.Errorf("FormatError = %q, want %q", got, tc.want)
			}
			for _, word := range banned {
				if strings.Contains(got, word) {
					t.Errorf("%q contains %q", got, word)
				}
			}
			if c := pgmi.ExitCodeForError(tc.err); c != tc.code {
				t.Errorf("exit code %d, want %d", c, tc.code)
			}
		})
	}
}

func TestFailureRecord_ACommentHeaderAboveATestKeepsTheLine(t *testing.T) {
	test := "-- Test: the header most tests carry\n/* and a block\n   comment */\nDO $$\nBEGIN\n    RAISE EXCEPTION 'TEST FAILED: x';\nEND $$;\n"
	scriptErr := newScriptError(&pgconn.PgError{
		Code: "P0001", Message: "TEST FAILED: x",
		Where: "PL/pgSQL function inline_code_block line 3 at RAISE\nSQL statement \"" + test + "\"\nPL/pgSQL function inline_code_block line 9 at EXECUTE",
	}, "deploy.sql", "DO $$ ... $$;", false)
	scriptErr.(*pgmi.ScriptError).Files = []pgmi.FileMetadata{{Path: "./__test__/test_x.sql", Content: test}}
	err := fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, scriptErr)
	d := pgmi.NewErrorDetail(err)
	if d.Line != 6 || d.SourceLine != "    RAISE EXCEPTION 'TEST FAILED: x';" {
		t.Fatalf("want the RAISE on line 6, got line %d %q", d.Line, d.SourceLine)
	}
	if out := pgmi.FormatError(err); !strings.Contains(out, "LOCATION: ./__test__/test_x.sql line 6") {
		t.Errorf("LOCATION must carry the line:\n%s", out)
	}
}

func TestFailureRecord_AParseErrorInsideAFunctionBodyHasItsFileLine(t *testing.T) {
	body := "\n    SELECT 1\n    FRM t;\n"
	file := "-- semantic/task.sql\nCREATE FUNCTION f() RETURNS int LANGUAGE sql AS $f$" + body + "$f$;\n"
	scriptErr := newScriptError(&pgconn.PgError{
		Code: "42601", Message: `syntax error at or near "FRM"`,
		InternalQuery: body, InternalPosition: int32(strings.Index(body, "FRM") + 1),
		Where: "SQL statement \"" + file + "\"\nPL/pgSQL function inline_code_block line 5 at EXECUTE",
	}, "deploy.sql", "DO $$ ... $$;", false)
	scriptErr.(*pgmi.ScriptError).Files = []pgmi.FileMetadata{{Path: "./semantic/task.sql", Content: file}}
	d := pgmi.NewErrorDetail(fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, scriptErr))
	if d.Script != "./semantic/task.sql" || d.Line != 4 || d.Column != 5 || d.SourceLine != "    FRM t;" {
		t.Fatalf("want ./semantic/task.sql line 4 column 5, got %+v", d)
	}
}

func TestFailureRecord_FailedFileNamesDeploySQLToo(t *testing.T) {
	script := "DO $$\nBEGIN\n    RAISE EXCEPTION 'missing parameter';\nEND $$;\n"
	scriptErr := newScriptError(&pgconn.PgError{
		Code: "P0001", Message: "missing parameter", Where: "PL/pgSQL function inline_code_block line 3 at RAISE",
	}, "deploy.sql", script, false)
	d := pgmi.NewErrorDetail(fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, scriptErr))
	if d.FailedFile != "deploy.sql" || d.Script != "deploy.sql" || d.Phase != "deploy" {
		t.Fatalf("a failure in deploy.sql must name it in failedFile too, got %+v", d)
	}
}

func newScriptError(err error, name, script string, expanded bool) error {
	return &pgmi.ScriptError{Err: err, Name: name, Script: script, Expanded: expanded}
}

func TestFailureRecord_AFileThatStartsWithADoBlock(t *testing.T) {
	content := "DO $$ BEGIN RAISE WARNING 'w'; END $$; SELECT 1/0;"
	files := []pgmi.FileMetadata{{Path: "./zzz.sql", Extension: ".sql", Content: content}}
	for _, c := range []struct {
		name  string
		where string
		line  int
	}{
		{"the error is outside the file's DO block", `SQL statement "` + content + `"` + "\nPL/pgSQL function inline_code_block line 10 at EXECUTE", 0},
		{"the error is inside it", "PL/pgSQL function inline_code_block line 1 at RAISE\n" +
			`SQL statement "` + content + `"` + "\nPL/pgSQL function inline_code_block line 10 at EXECUTE", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			scriptErr := newScriptError(&pgconn.PgError{Code: "22012", Message: "division by zero", Where: c.where},
				"deploy.sql", "BEGIN;\n"+strings.Repeat("\n", 8)+"DO $$ ... $$;\n", false)
			scriptErr.(*pgmi.ScriptError).Files = files
			d := pgmi.NewErrorDetail(fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, scriptErr))
			if d.FailedFile != "./zzz.sql" || d.Line != c.line {
				t.Errorf("want ./zzz.sql line %d, got %q line %d", c.line, d.FailedFile, d.Line)
			}
		})
	}
}
