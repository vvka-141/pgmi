package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestConfigWithoutATerminalIsAUsageError(t *testing.T) {
	orig := isInteractive
	t.Cleanup(func() { isInteractive = orig })
	isInteractive = func() bool { return false }

	err := runConfig(configCmd, []string{t.TempDir()})
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitUsageError {
		t.Fatalf("exit %d, want %d: %v", got, pgmi.ExitUsageError, err)
	}
}

func TestMetadataScaffoldPreviewGoesToStdout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new_file.sql"), []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := withRootArgs(t, "metadata", "scaffold", dir, "--sort-key", "./x/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "./new_file.sql") || !strings.Contains(out, "Sort key: ./x/new_file.sql") {
		t.Errorf("the per-file preview must go to stdout, got:\n%s", out)
	}
}

func TestParamPairsThatDifferOnlyInCaseAreRefused(t *testing.T) {
	_, err := loadMergedParameters(&config.ProjectConfig{}, paramSources{pairs: []string{"A=1", "a=2"}}, false)
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError || !strings.Contains(err.Error(), "differ only in case") {
		t.Fatalf("exit %d, want %d naming the collision: %v", got, pgmi.ExitConfigError, err)
	}
	merged, err := loadMergedParameters(&config.ProjectConfig{}, paramSources{pairs: []string{"a=1", "a=2"}}, false)
	if err != nil || merged["a"] != "2" {
		t.Errorf("a repeated key keeps its last value, got %v, %v", merged, err)
	}
}

func TestDeployDashHHintsThatItIsHost(t *testing.T) {
	_, err := withRootArgs(t, "deploy", "-h")
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitUsageError || !strings.Contains(err.Error(), "-h is --host here; use --help") {
		t.Fatalf("exit %d, want %d with the hint: %v", got, pgmi.ExitUsageError, err)
	}
}
