package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// reexecEnv makes the test binary behave as pgmi when it is run as a subprocess.
const reexecEnv = "PGMI_TEST_REEXEC"

// TestMain lets a subprocess of this test binary run the real CLI. Each test
// used to shell out to `go build` for its own copy of pgmi, which cost tens of
// seconds apiece and — worse — ran the Go toolchain while the outer
// `go test ./...` was itself compiling, contending on the build cache. That is
// how this package reached the 10m -race timeout with a goroutine stuck in
// exec.(*Cmd).writerDescriptor reading `go build`'s output.
//
// Re-execing ourselves removes the toolchain from the test path entirely, and
// the child inherits whatever instrumentation the suite was built with, so a
// race or a panic on the CLI path is still detected.
func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// pgmiCommand builds a command that runs this test binary as pgmi.
func pgmiCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	cmd := exec.Command(self, args...)
	cmd.Env = append(os.Environ(), reexecEnv+"=1")
	return cmd
}

func TestMain_Version_ExitZero(t *testing.T) {
	err := pgmiCommand(t, "--version").Run()

	if err != nil {
		t.Fatalf("Expected exit 0, got error: %v", err)
	}
}

// TestMain_Interrupt_ExitCode130 drives the real signal path end to end: no test
// covered it, and ExitCodeForError(context.Canceled) only pins the classifier,
// not the code that has to hand it context.Canceled in the first place. If the
// re-wrap in runDeploy regressed, a Ctrl-C where the deployer returns nil would
// exit 0 and every other test would still pass.
func TestMain_Interrupt_ExitCode130(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os/exec cannot deliver os.Interrupt to a child process on Windows")
	}

	// A listener that completes the TCP handshake and then says nothing: the
	// child blocks in the startup handshake, so the interrupt lands while a
	// deployment is genuinely in flight.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		close(accepted)
		<-time.After(30 * time.Second)
		conn.Close()
	}()

	projectDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(projectDir, "deploy.sql"), []byte("SELECT 1;\n"), 0644,
	); err != nil {
		t.Fatalf("writing deploy.sql: %v", err)
	}

	cmd := pgmiCommand(t, "deploy", projectDir,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port),
		"--database", "irrelevant",
		"--username", "irrelevant",
		"--timeout", "60s",
		"--force",
	)
	cmd.Env = append(cmd.Env, "PGPASSWORD=", "PGSERVICE=", "PGSERVICEFILE=")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting pgmi: %v", err)
	}

	select {
	case <-accepted:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("pgmi never reached the listener; the interrupt would not have landed mid-deploy")
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("sending SIGINT: %v", err)
	}

	err = cmd.Wait()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected a non-zero exit after SIGINT, got: %v", err)
	}
	if exitErr.ExitCode() != pgmi.ExitInterrupted {
		t.Errorf("Ctrl-C exited %d, want %d — an aborted run must not look like any other outcome",
			exitErr.ExitCode(), pgmi.ExitInterrupted)
	}
}

// TestMain_OverwriteWithoutForce_DoesNotHangOnOpenStdin reproduces the shape a
// CI runner actually has. Against /dev/null the confirmation prompt gets an
// immediate EOF and exits 12 cleanly — which is why this survived. Against an
// open stdin that never answers, the prompt blocked for the whole of --timeout
// and the run then reported exit 16, "operation exceeded --timeout", sending the
// operator to hunt a slow database instead of a missing --force.
//
// `go test` alone cannot show this: its own stdin is not an open-but-silent
// pipe, so the in-process test in internal/cli exits 12 either way. Only a
// subprocess with a real pipe reproduces the hang.
func TestMain_OverwriteWithoutForce_DoesNotHangOnOpenStdin(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	const testDB = "pgmi_overwrite_openstdin"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	projectDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(projectDir, "deploy.sql"), []byte("SELECT 1;\n"), 0644,
	); err != nil {
		t.Fatalf("writing deploy.sql: %v", err)
	}

	// The prompt only appears when the target already exists.
	seed := pgmiCommand(t, "deploy", projectDir,
		"--connection", connString, "-d", testDB, "--force", "--timeout", "60s")
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seeding the target database: %v\n%s", err, out)
	}

	cmd := pgmiCommand(t, "deploy", projectDir,
		"--connection", connString, "-d", testDB,
		"--overwrite", // and deliberately no --force
		"--timeout", "30s")

	// An open pipe nobody writes to: the CI shape, not /dev/null.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	defer stdin.Close()

	err = cmd.Run()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected a non-zero exit, got: %v", err)
	}

	// The exit code carries the whole property. Blocking on stdin that nobody
	// writes to means waiting out --timeout, and that exits 16, not 12 — pgmi
	// cannot hang here, so there is nothing a stopwatch adds.
	//
	// There used to be one, bounding this at 15s. It measured the machine
	// rather than pgmi: this test builds and runs two real subprocesses against
	// a real database, which takes ~1.5s alone and took 66s inside a loaded
	// full-suite run. A 10x margin over a second and a half of contended work
	// is not a margin.
	if exitErr.ExitCode() != pgmi.ExitApprovalDenied {
		t.Errorf("exit code %d, want %d (approval denied); %d would mean it waited out --timeout",
			exitErr.ExitCode(), pgmi.ExitApprovalDenied, pgmi.ExitTimeout)
	}
}
