package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
// The envelope is a machine contract on stdout specifically — the human summary
// goes to stderr — so the test has to read the same stream a CI parser does.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("closing read end: %v", err)
	}
	return out
}

func decodeEnvelope(t *testing.T, out string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope is not valid JSON: %v\ngot:\n%s", err, out)
	}
	return env
}

var sampleResult = &pgmi.DeployResult{
	FilesLoaded: 3,
	TestMacros:  1,
	Duration:    1165 * time.Millisecond,
	Database:    "myapp",
}

// TestDeployJSON_EnvelopeShape pins the keys documented in docs/CLI.md. Renaming
// "error" to "message", dropping "exitCode", or moving the envelope to stderr
// breaks every CI parser, and until now no test noticed.
func TestDeployJSON_EnvelopeShape(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		env := decodeEnvelope(t, captureStdout(t, func() {
			printDeployJSON(sampleResult, nil, deployProvenance{})
		}))

		want := map[string]any{
			"status":      "success",
			"exitCode":    float64(0),
			"filesLoaded": float64(3),
			"testMacros":  float64(1),
			"durationMs":  float64(1165),
			"database":    "myapp",
			"created":     false,
			"recreated":   false,
		}
		for k, v := range want {
			if env[k] != v {
				t.Errorf("envelope[%q] = %#v, want %#v", k, env[k], v)
			}
		}
		if _, ok := env["notices"].([]any); !ok {
			t.Errorf("envelope[\"notices\"] = %#v, want an array", env["notices"])
		}

		// Diagnostics are omitted, not emitted empty — a parser testing for
		// presence must not see a key on a clean run.
		for _, k := range []string{"error", "sqlstate", "detail", "hint", "where",
			"failedFile", "script", "sourceLine", "line", "column", "scriptExpanded",
			"executionMode"} {
			if _, ok := env[k]; ok {
				t.Errorf("successful run emitted diagnostic key %q = %#v", k, env[k])
			}
		}
	})

	t.Run("failure carries the postgres diagnostics it has and omits the rest", func(t *testing.T) {
		pgErr := &pgconn.PgError{
			Severity: "ERROR",
			Code:     "42883",
			Message:  "function this_function_does_not_exist() does not exist",
			Hint:     "No function matches the given name and argument types.",
			// Detail and Where deliberately empty.
		}
		deployErr := fmt.Errorf("execution failed: %w", pgErr)

		env := decodeEnvelope(t, captureStdout(t, func() {
			printDeployJSON(sampleResult, deployErr, deployProvenance{})
		}))

		if env["status"] != "failed" {
			t.Errorf(`status = %#v, want "failed"`, env["status"])
		}
		if env["exitCode"] != float64(pgmi.ExitCodeForError(deployErr)) {
			t.Errorf("exitCode = %#v, want %d", env["exitCode"], pgmi.ExitCodeForError(deployErr))
		}
		if env["sqlstate"] != "42883" {
			t.Errorf("sqlstate = %#v, want %q", env["sqlstate"], "42883")
		}
		if env["hint"] != pgErr.Hint {
			t.Errorf("hint = %#v, want %q", env["hint"], pgErr.Hint)
		}
		if msg, _ := env["error"].(string); msg == "" {
			t.Error(`the failure message must be under the key "error", not "message"`)
		}
		for _, k := range []string{"detail", "where"} {
			if _, ok := env[k]; ok {
				t.Errorf("empty diagnostic %q was emitted as %#v instead of omitted", k, env[k])
			}
		}

		// The run summary survives a failure — CLI.md documents it on both paths.
		if env["database"] != "myapp" || env["durationMs"] != float64(1165) {
			t.Errorf("failure envelope dropped the run summary: %#v", env)
		}
	})

	t.Run("passwords are redacted", func(t *testing.T) {
		deployErr := errors.New(
			`failed to connect: host=db port=5432 password=hunter2 sslmode=require`)

		out := captureStdout(t, func() { printDeployJSON(nil, deployErr, deployProvenance{}) })

		if strings.Contains(out, "hunter2") {
			t.Errorf("envelope leaked a password:\n%s", out)
		}
		env := decodeEnvelope(t, out)
		if msg, _ := env["error"].(string); !strings.Contains(msg, "password=[redacted]") {
			t.Errorf("expected the password to be replaced, got %q", msg)
		}
	})
}

// TestDeployJSON_ExitCodeMatchesProcessExit is the regression guard for the
// ordering defect: the envelope used to be emitted before the SIGINT re-wrap, so
// a Ctrl-C where the deployer returned nil published
// {"status":"success","exitCode":0} and the process then exited 130.
func TestDeployJSON_ExitCodeMatchesProcessExit(t *testing.T) {
	tests := []struct {
		name        string
		deployErr   error
		interrupted bool
		wantStatus  string
		wantExit    int
	}{
		{
			name:        "interrupted while the deployer reported success",
			deployErr:   nil,
			interrupted: true,
			wantStatus:  "failed",
			wantExit:    pgmi.ExitInterrupted,
		},
		{
			name:        "interrupted with an unrelated error in flight",
			deployErr:   errors.New("execution failed: connection reset"),
			interrupted: true,
			wantStatus:  "failed",
			wantExit:    pgmi.ExitInterrupted,
		},
		{
			name:        "not interrupted, clean run",
			deployErr:   nil,
			interrupted: false,
			wantStatus:  "success",
			wantExit:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var returned error
			var emitted bool

			out := captureStdout(t, func() {
				returned, emitted = finishDeploy(sampleResult, tt.deployErr, tt.interrupted, true, deployProvenance{})
			})

			if !emitted {
				t.Fatal("--json was requested but no envelope was emitted")
			}
			env := decodeEnvelope(t, out)

			// The contract: what the envelope publishes is what the process exits with.
			processExit := pgmi.ExitCodeForError(returned)
			if env["exitCode"] != float64(processExit) {
				t.Errorf("envelope exitCode %#v contradicts the process exit %d — "+
					"automation would record the wrong outcome", env["exitCode"], processExit)
			}
			if processExit != tt.wantExit {
				t.Errorf("process exit = %d, want %d (returned %v)", processExit, tt.wantExit, returned)
			}
			if env["status"] != tt.wantStatus {
				t.Errorf("status = %#v, want %q", env["status"], tt.wantStatus)
			}
			if tt.interrupted && !errors.Is(returned, context.Canceled) {
				t.Errorf("an interrupted run must return a context.Canceled chain, got %v", returned)
			}
		})
	}
}

func TestDeployJSON_TailFailureCarriesUnitContext(t *testing.T) {
	tailResult := &pgmi.DeployResult{
		FilesLoaded:    5,
		TestMacros:     0,
		Duration:       720 * time.Millisecond,
		Database:       "prod",
		ExecutionUnits: 4,
		UnitsCommitted: 2,
		ExecutionMode:  "psql",
	}
	deployErr := errors.New("execution failed: boom")

	env := decodeEnvelope(t, captureStdout(t, func() {
		printDeployJSON(tailResult, deployErr, deployProvenance{})
	}))

	if env["executionUnits"] != float64(4) {
		t.Errorf("executionUnits = %#v, want 4", env["executionUnits"])
	}
	if env["unitsCommitted"] != float64(2) {
		t.Errorf("unitsCommitted = %#v, want 2", env["unitsCommitted"])
	}
	if env["executionMode"] != "psql" {
		t.Errorf("executionMode = %#v, want %q", env["executionMode"], "psql")
	}
}

func TestDeployJSON_AtomicFailureCarriesUnitContext(t *testing.T) {
	atomicResult := &pgmi.DeployResult{
		FilesLoaded:    5,
		TestMacros:     0,
		Duration:       720 * time.Millisecond,
		Database:       "prod",
		ExecutionUnits: 3,
		UnitsCommitted: 0,
		ExecutionMode:  "atomic",
	}
	deployErr := errors.New("execution failed: syntax error")

	env := decodeEnvelope(t, captureStdout(t, func() {
		printDeployJSON(atomicResult, deployErr, deployProvenance{})
	}))

	if env["executionUnits"] != float64(3) {
		t.Errorf("executionUnits = %#v, want 3", env["executionUnits"])
	}
	if env["unitsCommitted"] != float64(0) {
		t.Errorf("unitsCommitted = %#v, want 0", env["unitsCommitted"])
	}
	if env["executionMode"] != "atomic" {
		t.Errorf("executionMode = %#v, want %q", env["executionMode"], "atomic")
	}
}

func TestDeploySummary_TailFailureMentionsUnitOrdinal(t *testing.T) {
	tailResult := &pgmi.DeployResult{
		FilesLoaded:    5,
		Duration:       720 * time.Millisecond,
		Database:       "prod",
		ExecutionUnits: 4,
		UnitsCommitted: 2,
		FailedUnit:     3,
		ExecutionMode:  "psql",
	}
	out := captureStderr(t, func() {
		printDeploySummary(tailResult, errors.New("boom"))
	})
	if !strings.Contains(out, "unit 3 of 4") {
		t.Errorf("tail failure summary should mention the failing unit ordinal:\n%s", out)
	}
	if !strings.Contains(out, "2 earlier unit(s) already committed") {
		t.Errorf("tail failure summary should mention committed units:\n%s", out)
	}
}

func TestDeploySummary_AtomicFailureOmitsUnitDetail(t *testing.T) {
	atomicResult := &pgmi.DeployResult{
		FilesLoaded:    5,
		Duration:       720 * time.Millisecond,
		Database:       "prod",
		ExecutionUnits: 3,
		UnitsCommitted: 0,
		ExecutionMode:  "atomic",
	}
	out := captureStderr(t, func() {
		printDeploySummary(atomicResult, errors.New("boom"))
	})
	if strings.Contains(out, "unit") {
		t.Errorf("atomic failure summary should NOT mention units (nothing committed):\n%s", out)
	}
	if !strings.Contains(out, "failed after 0.72s — nothing committed\n") {
		t.Errorf("an atomic failure must say nothing was committed:\n%s", out)
	}
}

func TestDeploySummary_NamesADatabaseItCreated(t *testing.T) {
	for _, tt := range []struct {
		mode, want string
	}{
		{"atomic", `— nothing committed; created database "fresh", left empty` + "\n"},
		{"", `— created database "fresh", left empty` + "\n"},
		{"psql", `already committed) — created database "fresh"` + "\n"},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			result := &pgmi.DeployResult{Duration: time.Second, Database: "fresh", Created: true, ExecutionMode: tt.mode,
				ExecutionUnits: 3, UnitsCommitted: 1, FailedUnit: 2}
			out := captureStderr(t, func() { printDeploySummary(result, errors.New("boom")) })
			if !strings.Contains(out, tt.want) {
				t.Errorf("want %q in:\n%s", tt.want, out)
			}
		})
	}
	recreated := captureStderr(t, func() {
		printDeploySummary(&pgmi.DeployResult{Duration: time.Second, Database: "kept", Recreated: true, ExecutionMode: "atomic"},
			errors.New("boom"))
	})
	if !strings.Contains(recreated, `— nothing committed; recreated database "kept", left empty`+"\n") {
		t.Errorf("a failed --overwrite must say the database was recreated and left empty:\n%s", recreated)
	}
	ok := captureStderr(t, func() { printDeploySummary(&pgmi.DeployResult{Database: "fresh", Created: true}, nil) })
	if strings.Contains(ok, "created") || strings.Contains(ok, "committed") {
		t.Errorf("the success line is unchanged:\n%s", ok)
	}
}

// TestDeploySummary_NamesTheTargetDatabase guards the observability half of
// PGMI-270. The only database line pgmi printed was "Database %q does not exist;
// creating", so a deploy into an existing database never said which one — and a
// connection string ending in /postgres silently outranking pgmi.yaml produced a
// green banner with nothing on screen to contradict it.
func TestDeploySummary_NamesTheTargetDatabase(t *testing.T) {
	tests := []struct {
		name      string
		deployErr error
	}{
		{"success", nil},
		{"failure", errors.New("execution failed: boom")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				printDeploySummary(sampleResult, tt.deployErr)
			})
			if !strings.Contains(out, sampleResult.Database) {
				t.Errorf("the summary does not name the target database %q:\n%s",
					sampleResult.Database, out)
			}
		})
	}
}

// captureStderr mirrors captureStdout: the human summary goes to stderr so
// `pgmi deploy . --json 2>/dev/null` still yields clean JSON.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("closing read end: %v", err)
	}
	return out
}

// The success line reports what pgmi knows, files loaded. "N test macro(s)
// expanded" read as "N tests ran" and stayed at 1 however many tests there
// were (PGMI-386).
func TestDeploySummary_SuccessWording(t *testing.T) {
	out := captureStderr(t, func() { printDeploySummary(sampleResult, nil) })
	want := fmt.Sprintf("%s: %d files loaded in ", sampleResult.Database, sampleResult.FilesLoaded)
	if !strings.Contains(out, want) || strings.Contains(out, "macro") {
		t.Errorf("summary = %q, want it to contain %q and no macro count", out, want)
	}
}

func TestFinishDeploy_JSONKeepsStderrSummary(t *testing.T) {
	for _, tt := range []struct {
		name   string
		result *pgmi.DeployResult
		err    error
		want   string
	}{
		{"success", &pgmi.DeployResult{Duration: time.Second, Database: "ok", ExecutionMode: "atomic", ExecutionUnits: 1, UnitsCommitted: 1}, nil, "ok ok"},
		{"overwrite failure", &pgmi.DeployResult{Duration: time.Second, Database: "kept", Recreated: true, ExecutionMode: "atomic"},
			errors.New("boom"), `recreated database "kept", left empty`},
		{"tail failure", &pgmi.DeployResult{Duration: time.Second, Database: "prod", ExecutionUnits: 4, UnitsCommitted: 2, FailedUnit: 3,
			ExecutionMode: "psql"}, errors.New("boom"), "2 earlier unit(s) already committed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plain := captureStderr(t, func() { _, _ = finishDeploy(tt.result, tt.err, false, false, deployProvenance{}) })
			var stdout string
			withJSON := captureStderr(t, func() {
				stdout = captureStdout(t, func() { _, _ = finishDeploy(tt.result, tt.err, false, true, deployProvenance{}) })
			})
			if !strings.Contains(plain, tt.want) || withJSON != plain {
				t.Errorf("stderr with --json must match without it and contain %q:\nplain: %s\njson:  %s", tt.want, plain, withJSON)
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(stdout), &record); err != nil {
				t.Errorf("stdout must hold only the JSON record: %v\n%s", err, stdout)
			}
		})
	}
}

func TestCLIWording_MissingMaintenanceDatabaseNamesTheCLISources(t *testing.T) {
	lib := services.MissingMaintenanceDBError{Name: "nosuchmaint", Err: errors.New("3D000")}
	err := cliWording(fmt.Errorf("deploy: %w", lib))
	msg := err.Error()
	for _, want := range []string{`maintenance database "nosuchmaint" does not exist`, "connection string", "connection.maintenance_database"} {
		if !strings.Contains(msg, want) {
			t.Errorf("CLI message lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "DeploymentConfig") {
		t.Errorf("the CLI message names the library field: %s", msg)
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConnectionError {
		t.Errorf("exit code %d, want %d", got, pgmi.ExitConnectionError)
	}
}

// A parser reads the same fact keys whatever the exit code; a fact the run
// never reached is null. Only diagnostics depend on what failed.
func TestDeployJSON_FactKeysDoNotDependOnTheExitCode(t *testing.T) {
	diagnostic := map[string]bool{"error": true, "sqlstate": true, "detail": true, "hint": true, "where": true,
		"failedFile": true, "phase": true, "script": true, "sourceLine": true, "failedFileCandidates": true,
		"line": true, "column": true, "scriptExpanded": true, "executionUnits": true, "unitsCommitted": true, "executionMode": true}
	keys := func(result *pgmi.DeployResult, err error) []string {
		var ks []string
		for k := range deployResultFields(result, err, deployProvenance{}) {
			if !diagnostic[k] {
				ks = append(ks, k)
			}
		}
		slices.Sort(ks)
		return ks
	}
	want := keys(sampleResult, nil)
	for name, run := range map[string]struct {
		result *pgmi.DeployResult
		err    error
	}{
		"exit 10": {nil, fmt.Errorf("%w: no such path", pgmi.ErrInvalidConfig)},
		"exit 11": {nil, fmt.Errorf("%w: refused", pgmi.ErrConnectionFailed)},
		"exit 13": {sampleResult, fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, &pgconn.PgError{Code: "42P01", Message: "no"})},
		"exit 14": {nil, pgmi.ErrDeploySQLNotFound},
	} {
		if got := keys(run.result, run.err); !slices.Equal(got, want) {
			t.Errorf("%s: keys %v, want %v", name, got, want)
		}
	}
}
