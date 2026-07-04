package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func buildTestBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "pgmi")
	if os.PathSeparator == '\\' {
		binPath += ".exe"
	}
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build: %v\n%s", err, out)
	}
	return binPath
}

func TestMain_Version_ExitZero(t *testing.T) {
	binPath := buildTestBinary(t)

	cmd := exec.Command(binPath, "--version")
	err := cmd.Run()

	if err != nil {
		t.Fatalf("Expected exit 0, got error: %v", err)
	}
}

func TestMain_PanicRecovery_ExitCode3(t *testing.T) {
	binPath := buildTestBinary(t)

	cmd := exec.Command(binPath, "version")
	cmd.Env = append(os.Environ(), "PGMI_TEST_PANIC=1")

	err := cmd.Run()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("Expected ExitError, got: %v", err)
	}

	if exitErr.ExitCode() != pgmi.ExitPanic {
		t.Errorf("Expected exit code %d (panic), got %d", pgmi.ExitPanic, exitErr.ExitCode())
	}
}
