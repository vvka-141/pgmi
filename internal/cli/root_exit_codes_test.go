package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// withRootArgs drives pgmi's real command tree. Nothing else in the repo does:
// init_test.go calls rootCmd.SetArgs and then invokes RunE directly, so the
// SetArgs is vestigial and the parse layer is never exercised. That is the same
// blind spot that let `-h` claim help and exit 0 without deploying (PGMI-217).
func withRootArgs(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetDeployFlags()
	resetCommandFlags(t)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		resetDeployFlags()
	})

	err := Execute()
	return out.String(), err
}

// resetCommandFlags restores every flag on the tree to its default. Cobra keeps
// parsed values on the command, so one test's `--help` stays set for the next
// Execute in the same process and silently turns it into a help request.
func resetCommandFlags(t *testing.T) {
	t.Helper()
	reset := func(c *cobra.Command) {
		for _, set := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags()} {
			set.VisitAll(func(f *pflag.Flag) {
				if f.Changed {
					if err := f.Value.Set(f.DefValue); err != nil {
						t.Fatalf("resetting --%s: %v", f.Name, err)
					}
					f.Changed = false
				}
			})
		}
	}
	reset(rootCmd)
	for _, c := range rootCmd.Commands() {
		reset(c)
	}
	t.Cleanup(func() {
		reset(rootCmd)
		for _, c := range rootCmd.Commands() {
			reset(c)
		}
	})
}

func TestRootCmd_UsageErrorsExitTwo(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown command", []string{"frobnicate"}},
		{"unknown flag", []string{"deploy", ".", "--nope"}},
		{"too many args", []string{"deploy", "a", "b"}},
		{"missing required arg", []string{"deploy"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := withRootArgs(t, tt.args...)
			if err == nil {
				t.Fatalf("pgmi %s returned no error", strings.Join(tt.args, " "))
			}
			if got := pgmi.ExitCodeForError(err); got != pgmi.ExitUsageError {
				t.Errorf("exit code %d, want %d (usage): %v", got, pgmi.ExitUsageError, err)
			}
		})
	}
}

// TestHelpFlagStillShowsHelp guards the other half of PGMI-217. pgmi registers
// its own --help bool at root.go so cobra cannot claim -h, which belongs to
// --host across the PostgreSQL toolchain. If that flag is renamed or scoped,
// cobra stops recognising --help as a help request and `pgmi deploy . --help`
// parses it as an unknown flag or, worse, runs the deploy.
func TestHelpFlagStillShowsHelp(t *testing.T) {
	out, err := withRootArgs(t, "deploy", "--help")
	if err != nil {
		t.Fatalf("deploy --help must not error: %v", err)
	}
	if !strings.Contains(out, "Exit codes:") {
		t.Errorf("deploy --help did not print the command's help; got:\n%s", out)
	}
	if deployFlags.database != "" || deployFlags.host != "" {
		t.Errorf("deploy --help populated connection flags (database=%q host=%q) — it parsed as a deploy",
			deployFlags.database, deployFlags.host)
	}
}

// -h is the host. Reading it as help is the PGMI-217 regression itself.
func TestShortHostFlagIsNotHelp(t *testing.T) {
	clearPGEnv(t)

	_, err := withRootArgs(t, "deploy", t.TempDir(),
		"-h", "127.0.0.1", "-d", "app", "--timeout", "1s")

	if err == nil {
		t.Fatal("-h 127.0.0.1 exited 0 — it was read as --help and nothing was deployed")
	}
	if deployFlags.host != "127.0.0.1" {
		t.Errorf("-h did not reach --host, deployFlags.host = %q", deployFlags.host)
	}
}

// TestDeployCmd_MissingDeploySQL_ExitCode14 pins the ordering in
// services/deployer.go: the project is scanned before the database is created,
// so a typo'd path cannot leave a freshly created database behind. An
// unreachable host makes 11 the competing answer, and 14 must win.
func TestDeployCmd_MissingDeploySQL_ExitCode14(t *testing.T) {
	resetDeployFlags()
	clearPGEnv(t)

	deployFlags.host = "nonexistent.invalid"
	deployFlags.port = 5432
	deployFlags.database = "testdb"
	deployFlags.username = "testuser"
	deployFlags.timeout = 10 * time.Second

	err := runDeploy(deployCmd, []string{t.TempDir()})
	if err == nil {
		t.Fatal("a project without deploy.sql must not deploy")
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitDeploySQLMissing {
		t.Errorf("exit code %d, want %d (deploy.sql not found) — a missing project file must be "+
			"reported before the connection is attempted: %v", got, pgmi.ExitDeploySQLMissing, err)
	}
}

// TestRunDeploy_CancelledWizardIsExit12 reaches a branch that is otherwise dead
// under `go test`: the wizard only runs when the terminal is interactive.
// Cancelling it deployed nothing, so exiting 0 would report success to a caller
// that got no deployment.
func TestRunDeploy_CancelledWizardIsExit12(t *testing.T) {
	resetDeployFlags()
	clearPGEnv(t)

	origInteractive, origWizard := isInteractive, runConnectionWizard
	t.Cleanup(func() { isInteractive, runConnectionWizard = origInteractive, origWizard })

	isInteractive = func() bool { return true }
	wizardCalled := false
	runConnectionWizard = func(string) (*pgmi.ConnectionConfig, error) {
		wizardCalled = true
		return nil, nil // cancelled
	}

	err := runDeploy(deployCmd, []string{deployProjectDir(t)})

	if !wizardCalled {
		t.Fatal("the wizard was never reached; the test is not exercising the branch it claims to")
	}
	if err == nil {
		t.Fatal("a cancelled connection wizard exited 0")
	}
	if !errors.Is(err, pgmi.ErrApprovalDenied) {
		t.Errorf("expected an ErrApprovalDenied chain, got %v", err)
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitApprovalDenied {
		t.Errorf("exit code %d, want %d (approval denied): %v", got, pgmi.ExitApprovalDenied, err)
	}
}

// TestDeployCmd_SQLFailureIsExit13 covers 13 at the CLI boundary. It was pinned
// only inside the service layer, so the classification could have been lost
// anywhere between there and the process exit and no test would have noticed.
func TestDeployCmd_SQLFailureIsExit13(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	resetDeployFlags()
	clearPGEnv(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"),
		[]byte("SELECT this_function_does_not_exist();\n"), 0644); err != nil {
		t.Fatalf("writing deploy.sql: %v", err)
	}

	testDB := "pgmi_cli_exit13"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	deployFlags.connection = connString
	deployFlags.database = testDB
	deployFlags.overwrite = true
	deployFlags.force = true
	deployFlags.timeout = 60 * time.Second

	err := runDeploy(deployCmd, []string{dir})
	if err == nil {
		t.Fatal("a deploy.sql that raises must not exit 0")
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitExecutionFailed {
		t.Errorf("exit code %d, want %d (SQL execution failed): %v",
			got, pgmi.ExitExecutionFailed, err)
	}
}

// orchestrationFlagNames and orchestrationFlagShapes encode the CLI philosophy
// from CLAUDE.md: pgmi's flags are infrastructure only. Transaction boundaries,
// execution filtering and rollback belong in deploy.sql, which is what makes
// pgmi more capable than a tool that needs --dry-run, not less.
var (
	orchestrationFlagNames = map[string]string{
		"dry-run":   "users control transactions in deploy.sql (BEGIN/ROLLBACK, or a preview parameter)",
		"quiet":     "console output is PostgreSQL notices; users control it with RAISE NOTICE and client_min_messages",
		"rollback":  "rollback logic belongs in deploy.sql, where the user decides when and how",
		"test-only": "test execution belongs in deploy.sql via CALL pgmi_test()",
	}
	orchestrationFlagShapes = regexp.MustCompile(`^(skip|only|no)-`)
)

// TestDeployCmdHasNoOrchestrationFlags makes the invariant executable. deploy
// registers two dozen flags and, until now, nothing constrained the set — the
// rule lived only in review prose, in a repo where agents write most of the code.
func TestDeployCmdHasNoOrchestrationFlags(t *testing.T) {
	deployCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if why, banned := orchestrationFlagNames[f.Name]; banned {
			t.Errorf("--%s controls deployment behavior: %s", f.Name, why)
		}
		if orchestrationFlagShapes.MatchString(f.Name) {
			t.Errorf("--%s filters execution; that belongs in a deploy.sql WHERE clause", f.Name)
		}
	})
}

// TestDeployCmd_OverwriteWithoutForceIsExit12WhenNonInteractive covers the
// shape a CI runner actually has. Against /dev/null the prompt gets an immediate
// EOF and exits 12 cleanly, which is why this survived; against an open stdin
// that never answers it blocked for the whole of --timeout and then reported
// exit 16, "operation exceeded --timeout", for a missing --force.
//
// `go test` is itself non-interactive, so the unfixed code hangs here for the
// full --timeout below and fails on the elapsed-time assertion.
func TestDeployCmd_OverwriteWithoutForceIsExit12WhenNonInteractive(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	const testDB = "pgmi_overwrite_noninteractive"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte("SELECT 1;\n"), 0644); err != nil {
		t.Fatalf("writing deploy.sql: %v", err)
	}

	// The approval prompt only appears when the target already exists.
	resetDeployFlags()
	clearPGEnv(t)
	deployFlags.connection = connString
	deployFlags.database = testDB
	deployFlags.force = true
	deployFlags.timeout = 60 * time.Second
	if err := runDeploy(deployCmd, []string{dir}); err != nil {
		t.Fatalf("seeding the target database: %v", err)
	}

	resetDeployFlags()
	deployFlags.connection = connString
	deployFlags.database = testDB
	deployFlags.overwrite = true // and deliberately no --force
	deployFlags.timeout = 30 * time.Second

	start := time.Now()
	err := runDeploy(deployCmd, []string{dir})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("--overwrite proceeded with nobody to confirm it")
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitApprovalDenied {
		t.Errorf("exit code %d, want %d (approval denied): %v", got, pgmi.ExitApprovalDenied, err)
	}
	// The defect is the wait, not only the code: --timeout is documented as
	// catastrophic-failure protection, and here it was load-bearing for a
	// forgotten flag.
	if elapsed > 5*time.Second {
		t.Errorf("the refusal took %v — it waited on input that will never arrive", elapsed)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the error does not name the fix: %v", err)
	}
}
