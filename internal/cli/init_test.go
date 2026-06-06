package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInit_BasicTemplate(t *testing.T) {
	targetDir := t.TempDir()
	projectDir := filepath.Join(targetDir, "myapp")

	initTemplate = "basic"
	rootCmd.SetArgs([]string{"init", projectDir})
	err := initCmd.RunE(initCmd, []string{projectDir})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	deploySQL := filepath.Join(projectDir, "deploy.sql")
	if _, err := os.Stat(deploySQL); os.IsNotExist(err) {
		t.Error("Expected deploy.sql to exist")
	}
}

func TestRunInit_AdvancedTemplate(t *testing.T) {
	targetDir := t.TempDir()
	projectDir := filepath.Join(targetDir, "myapp")

	initTemplate = "advanced"
	rootCmd.SetArgs([]string{"init", projectDir})
	err := initCmd.RunE(initCmd, []string{projectDir})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	deploySQL := filepath.Join(projectDir, "deploy.sql")
	if _, err := os.Stat(deploySQL); os.IsNotExist(err) {
		t.Error("Expected deploy.sql to exist")
	}
}

func TestManagedCloudCaveat(t *testing.T) {
	if got := managedCloudCaveat("basic"); got != "" {
		t.Errorf("basic template should have no managed-cloud caveat, got %q", got)
	}

	adv := managedCloudCaveat("advanced")
	if adv == "" {
		t.Fatal("advanced template should emit a managed-cloud caveat")
	}
	for _, want := range []string{"entity-standards.sql", "superuser", "PRODUCTION.md#managed-cloud-postgresql"} {
		if !strings.Contains(adv, want) {
			t.Errorf("advanced caveat missing %q; got: %s", want, adv)
		}
	}
}

func TestRunInit_InvalidTemplate(t *testing.T) {
	targetDir := t.TempDir()
	projectDir := filepath.Join(targetDir, "myapp")

	initTemplate = "nonexistent"
	err := initCmd.RunE(initCmd, []string{projectDir})
	if err == nil {
		t.Fatal("Expected error for invalid template")
	}
	if !strings.Contains(err.Error(), "unknown template") {
		t.Errorf("Expected 'unknown template' error, got: %v", err)
	}
}

func TestRunInit_NonEmptyDirectory(t *testing.T) {
	targetDir := t.TempDir()
	os.WriteFile(filepath.Join(targetDir, "existing.txt"), []byte("data"), 0644)

	initTemplate = "basic"
	err := initCmd.RunE(initCmd, []string{targetDir})
	if err == nil {
		t.Fatal("Expected error for non-empty directory")
	}
}

func TestRunInit_CurrentDirectory(t *testing.T) {
	targetDir := t.TempDir()
	emptySubdir := filepath.Join(targetDir, "empty")
	os.MkdirAll(emptySubdir, 0755)

	initTemplate = "basic"
	err := initCmd.RunE(initCmd, []string{emptySubdir})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	deploySQL := filepath.Join(emptySubdir, "deploy.sql")
	if _, err := os.Stat(deploySQL); os.IsNotExist(err) {
		t.Error("Expected deploy.sql to exist")
	}
}
