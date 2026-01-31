package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetMetadataFlags() {
	scaffoldDryRun = true
	scaffoldWrite = false
	scaffoldIdempotent = true
	validateJSON = false
	planJSON = false
}

func TestMetadataScaffoldCmd_ArgsValidation(t *testing.T) {
	err := metadataScaffoldCmd.Args(metadataScaffoldCmd, []string{})
	if err == nil {
		t.Fatal("Expected error for missing args")
	}
}

func TestMetadataScaffoldCmd_ArgsValidation_TooMany(t *testing.T) {
	err := metadataScaffoldCmd.Args(metadataScaffoldCmd, []string{"a", "b"})
	if err == nil {
		t.Fatal("Expected error for too many args")
	}
}

func TestMetadataValidateCmd_ArgsValidation(t *testing.T) {
	err := metadataValidateCmd.Args(metadataValidateCmd, []string{})
	if err == nil {
		t.Fatal("Expected error for missing args")
	}
}

func TestMetadataPlanCmd_ArgsValidation(t *testing.T) {
	err := metadataPlanCmd.Args(metadataPlanCmd, []string{})
	if err == nil {
		t.Fatal("Expected error for missing args")
	}
}

func createTestProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("Failed to create dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write file: %v", err)
		}
	}
	return dir
}

func TestMetadataScaffold_DryRunDefault(t *testing.T) {
	resetMetadataFlags()
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql":          "SELECT 1;",
		"migrations/001.sql":  "CREATE TABLE t1(id int);",
	})

	err := runMetadataScaffold(metadataScaffoldCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	content, _ := os.ReadFile(filepath.Join(projectPath, "migrations", "001.sql"))
	if strings.Contains(string(content), "pgmi-meta") {
		t.Error("Expected dry-run to NOT modify files")
	}
}

func TestMetadataScaffold_WriteMode(t *testing.T) {
	resetMetadataFlags()
	scaffoldWrite = true

	projectPath := createTestProject(t, map[string]string{
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": "CREATE TABLE t1(id int);",
	})

	err := runMetadataScaffold(metadataScaffoldCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	content, _ := os.ReadFile(filepath.Join(projectPath, "migrations", "001.sql"))
	if !strings.Contains(string(content), "pgmi-meta") {
		t.Error("Expected write mode to add metadata to file")
	}
	if !strings.Contains(string(content), "CREATE TABLE t1(id int);") {
		t.Error("Expected original content to be preserved")
	}
}

func TestMetadataScaffold_AllFilesHaveMetadata(t *testing.T) {
	resetMetadataFlags()
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql": "SELECT 1;",
		"migrations/001.sql": `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>Test</description>
  <sortKeys><key>01</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t1(id int);`,
	})

	err := runMetadataScaffold(metadataScaffoldCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestMetadataScaffold_NonexistentPath(t *testing.T) {
	resetMetadataFlags()
	err := runMetadataScaffold(metadataScaffoldCmd, []string{"/nonexistent/path/xyz"})
	if err == nil {
		t.Fatal("Expected error for nonexistent path")
	}
}

func TestMetadataScaffold_IdempotentFlagFalse(t *testing.T) {
	resetMetadataFlags()
	scaffoldWrite = true
	scaffoldIdempotent = false

	projectPath := createTestProject(t, map[string]string{
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": "CREATE TABLE t1(id int);",
	})

	err := runMetadataScaffold(metadataScaffoldCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	content, _ := os.ReadFile(filepath.Join(projectPath, "migrations", "001.sql"))
	if !strings.Contains(string(content), `idempotent="false"`) {
		t.Error("Expected idempotent=false in generated metadata")
	}
}

func TestMetadataValidate_ValidProject(t *testing.T) {
	resetMetadataFlags()
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql": "SELECT 1;",
		"migrations/001.sql": `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>First migration</description>
  <sortKeys><key>01</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t1(id int);`,
		"migrations/002.sql": `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440001"
    idempotent="true">
  <description>Second migration</description>
  <sortKeys><key>02</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t2(id int);`,
	})

	err := runMetadataValidate(metadataValidateCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestMetadataValidate_DuplicateIDs(t *testing.T) {
	resetMetadataFlags()
	sameID := "550e8400-e29b-41d4-a716-446655440000"
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql": "SELECT 1;",
		"migrations/001.sql": `/*
<pgmi-meta
    id="` + sameID + `"
    idempotent="true">
  <description>First</description>
  <sortKeys><key>01</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t1(id int);`,
		"migrations/002.sql": `/*
<pgmi-meta
    id="` + sameID + `"
    idempotent="true">
  <description>Second</description>
  <sortKeys><key>02</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t2(id int);`,
	})

	err := runMetadataValidate(metadataValidateCmd, []string{projectPath})
	if err == nil {
		t.Fatal("Expected error for duplicate IDs")
	}
}

func TestMetadataValidate_JSONOutput(t *testing.T) {
	resetMetadataFlags()
	validateJSON = true

	projectPath := createTestProject(t, map[string]string{
		"deploy.sql": "SELECT 1;",
		"migrations/001.sql": `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>Migration</description>
  <sortKeys><key>01</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t1(id int);`,
	})

	err := runMetadataValidate(metadataValidateCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestMetadataValidate_NonexistentPath(t *testing.T) {
	resetMetadataFlags()
	err := runMetadataValidate(metadataValidateCmd, []string{"/nonexistent/path/xyz"})
	if err == nil {
		t.Fatal("Expected error for nonexistent path")
	}
}

func TestMetadataPlan_WithMetadata(t *testing.T) {
	resetMetadataFlags()
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql": "SELECT 1;",
		"migrations/001.sql": `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>First migration</description>
  <sortKeys><key>01-deploy</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t1(id int);`,
	})

	err := runMetadataPlan(metadataPlanCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestMetadataPlan_WithoutMetadata(t *testing.T) {
	resetMetadataFlags()
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": "CREATE TABLE t1(id int);",
	})

	err := runMetadataPlan(metadataPlanCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestMetadataPlan_JSONOutput(t *testing.T) {
	resetMetadataFlags()
	planJSON = true

	projectPath := createTestProject(t, map[string]string{
		"deploy.sql": "SELECT 1;",
		"migrations/001.sql": `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>Migration</description>
  <sortKeys><key>01</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t1(id int);`,
	})

	err := runMetadataPlan(metadataPlanCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestMetadataPlan_NonexistentPath(t *testing.T) {
	resetMetadataFlags()
	err := runMetadataPlan(metadataPlanCmd, []string{"/nonexistent/path/xyz"})
	if err == nil {
		t.Fatal("Expected error for nonexistent path")
	}
}

func TestMetadataPlan_MixedMetadata(t *testing.T) {
	resetMetadataFlags()
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql": "SELECT 1;",
		"migrations/001.sql": `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>With metadata</description>
  <sortKeys><key>01</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t1(id int);`,
		"migrations/002.sql": "CREATE TABLE t2(id int);",
	})

	err := runMetadataPlan(metadataPlanCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestMetadataValidate_FilesWithoutMetadata(t *testing.T) {
	resetMetadataFlags()
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": "CREATE TABLE t1(id int);",
	})

	err := runMetadataValidate(metadataValidateCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestMetadataValidate_DuplicateIDs_JSONOutput(t *testing.T) {
	resetMetadataFlags()
	validateJSON = true
	sameID := "550e8400-e29b-41d4-a716-446655440000"
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql": "SELECT 1;",
		"migrations/001.sql": `/*
<pgmi-meta
    id="` + sameID + `"
    idempotent="true">
  <description>First</description>
  <sortKeys><key>01</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t1(id int);`,
		"migrations/002.sql": `/*
<pgmi-meta
    id="` + sameID + `"
    idempotent="true">
  <description>Second</description>
  <sortKeys><key>02</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE t2(id int);`,
	})

	err := runMetadataValidate(metadataValidateCmd, []string{projectPath})
	if err == nil {
		t.Fatal("Expected error for duplicate IDs in JSON mode")
	}
}
