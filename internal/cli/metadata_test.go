package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func resetMetadataFlags() {
	scaffoldWrite = false
	scaffoldIdempotent = true
	scaffoldSortKey = "005/"
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
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": "CREATE TABLE t1(id int);",
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

func TestMetadataScaffold_Write_SkipsNonSQLFiles(t *testing.T) {
	resetMetadataFlags()
	scaffoldWrite = true

	jsonContent := `{"name": "test-project"}`
	yamlContent := "database: mydb\nhost: localhost\n"
	mdContent := "# README\nThis is a project.\n"

	projectPath := createTestProject(t, map[string]string{
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": "CREATE TABLE t1(id int);",
		"project.json":       jsonContent,
		"pgmi.yaml":          yamlContent,
		"README.md":          mdContent,
	})

	err := runMetadataScaffold(metadataScaffoldCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(projectPath, "project.json"))
	if string(got) != jsonContent {
		t.Errorf("project.json was corrupted: got %q", string(got))
	}
	got, _ = os.ReadFile(filepath.Join(projectPath, "pgmi.yaml"))
	if string(got) != yamlContent {
		t.Errorf("pgmi.yaml was corrupted: got %q", string(got))
	}
	got, _ = os.ReadFile(filepath.Join(projectPath, "README.md"))
	if string(got) != mdContent {
		t.Errorf("README.md was corrupted: got %q", string(got))
	}
}

func TestMetadataScaffold_Write_SkipsTestFiles(t *testing.T) {
	resetMetadataFlags()
	scaffoldWrite = true

	testContent := "SELECT 1 AS test_result;"

	projectPath := createTestProject(t, map[string]string{
		"deploy.sql":                     "SELECT 1;",
		"migrations/001.sql":             "CREATE TABLE t1(id int);",
		"migrations/__test__/test_1.sql": testContent,
	})

	err := runMetadataScaffold(metadataScaffoldCmd, []string{projectPath})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(projectPath, "migrations", "__test__", "test_1.sql"))
	if string(got) != testContent {
		t.Errorf("test file was modified: got %q", string(got))
	}
}

// pgmi_plan_view holds every loaded file, not just SQL; deploy.sql filters on
// is_sql_file. The offline plan lists the same rows and marks the non-SQL ones.
func TestMetadataPlan_FlagsNonSQLFiles(t *testing.T) {
	resetMetadataFlags()
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": "CREATE TABLE t1(id int);",
		"project.json":       `{"name": "test"}`,
		"pgmi.yaml":          "database: mydb\n",
		"README.md":          "# README\n",
	})

	result, err := planProject(projectPath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(result.Plan) != 4 {
		t.Fatalf("plan has %d rows, want 4 (deploy.sql is never loaded): %+v", len(result.Plan), result.Plan)
	}
	for _, entry := range result.Plan {
		if want := filepath.Ext(entry.Path) == ".sql"; entry.IsSQLFile != want {
			t.Errorf("%s: isSqlFile = %v, want %v", entry.Path, entry.IsSQLFile, want)
		}
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

// Every bad <pgmi-meta> block is reported in one run, each in --json errors.
func TestMetadataValidate_ReportsEveryBadFile(t *testing.T) {
	resetMetadataFlags()
	validateJSON = true
	bad := "/*\n<pgmi-meta id=\"not-a-uuid\" idempotent=\"true\"></pgmi-meta>\n*/\nSELECT 1;"
	projectPath := createTestProject(t, map[string]string{
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": bad,
		"migrations/002.sql": bad,
	})
	var out bytes.Buffer
	metadataValidateCmd.SetOut(&out)
	t.Cleanup(func() { metadataValidateCmd.SetOut(nil) })

	err := runMetadataValidate(metadataValidateCmd, []string{projectPath})
	if pgmi.ExitCodeForError(err) != pgmi.ExitConfigError {
		t.Fatalf("want exit %d, got %v", pgmi.ExitConfigError, err)
	}
	var env struct {
		Errors   []string `json:"errors"`
		ExitCode int      `json:"exitCode"`
	}
	if jerr := json.Unmarshal(out.Bytes(), &env); jerr != nil {
		t.Fatalf("parse %q: %v", out.String(), jerr)
	}
	if len(env.Errors) != 2 || !strings.Contains(strings.Join(env.Errors, "\n"), "001.sql") ||
		!strings.Contains(strings.Join(env.Errors, "\n"), "002.sql") || env.ExitCode != pgmi.ExitConfigError {
		t.Errorf("want both files in errors and exit %d, got %+v", pgmi.ExitConfigError, env)
	}
}

func TestDeployHelpNamesTheProjectDotEnv(t *testing.T) {
	if !strings.Contains(deployCmd.Long, "A project .env is loaded") || !strings.Contains(deployCmd.Long, "pgmi serve does not read it") {
		t.Errorf("pgmi help deploy must say a project .env is loaded and serve ignores it:\n%s", deployCmd.Long)
	}
}
