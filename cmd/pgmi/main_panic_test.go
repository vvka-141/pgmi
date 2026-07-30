//go:build pgmi_testhooks

package main

import (
	"os/exec"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestMain_PanicRecovery_ExitCode3(t *testing.T) {
	cmd := pgmiCommand(t, "version")
	cmd.Env = append(cmd.Env, "PGMI_TEST_PANIC=1")

	err := cmd.Run()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("Expected ExitError, got: %v", err)
	}

	if exitErr.ExitCode() != pgmi.ExitPanic {
		t.Errorf("Expected exit code %d (panic), got %d", pgmi.ExitPanic, exitErr.ExitCode())
	}
}
