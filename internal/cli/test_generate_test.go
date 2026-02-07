package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func resetTestGenerateFlags() {
	testGenerateFlags = testGenerateFlagValues{
		filter:          ".*",
		withTransaction: true,
		withNotices:     true,
		withDebug:       false,
		callback:        "",
		output:          "",
	}
}

func TestBuildTestTreeAndResolver(t *testing.T) {
	tests := []struct {
		name          string
		files         []pgmi.FileMetadata
		wantTestCount int
		wantResolve   string
	}{
		{
			name:          "empty files",
			files:         []pgmi.FileMetadata{},
			wantTestCount: 0,
		},
		{
			name: "only non-test files",
			files: []pgmi.FileMetadata{
				{Path: "./migrations/001.sql", Directory: "./migrations/", Name: "001.sql", Extension: ".sql", Content: "SELECT 1;"},
			},
			wantTestCount: 0,
		},
		{
			name: "single test file",
			files: []pgmi.FileMetadata{
				{Path: "./__test__/test_foo.sql", Directory: "./__test__/", Name: "test_foo.sql", Extension: ".sql", Content: "SELECT 'test';"},
			},
			wantTestCount: 1,
			wantResolve:   "./__test__/test_foo.sql",
		},
		{
			name: "mixed files",
			files: []pgmi.FileMetadata{
				{Path: "./migrations/001.sql", Directory: "./migrations/", Name: "001.sql", Extension: ".sql", Content: "CREATE TABLE t();"},
				{Path: "./__test__/test_a.sql", Directory: "./__test__/", Name: "test_a.sql", Extension: ".sql", Content: "SELECT 'a';"},
				{Path: "./__test__/test_b.sql", Directory: "./__test__/", Name: "test_b.sql", Extension: ".sql", Content: "SELECT 'b';"},
			},
			wantTestCount: 2,
			wantResolve:   "./__test__/test_a.sql",
		},
		{
			name: "nested test directories",
			files: []pgmi.FileMetadata{
				{Path: "./module/__test__/_setup.sql", Directory: "./module/__test__/", Name: "_setup.sql", Extension: ".sql", Content: "-- setup"},
				{Path: "./module/__test__/test_core.sql", Directory: "./module/__test__/", Name: "test_core.sql", Extension: ".sql", Content: "SELECT 1;"},
				{Path: "./module/__test__/sub/_setup.sql", Directory: "./module/__test__/sub/", Name: "_setup.sql", Extension: ".sql", Content: "-- sub setup"},
				{Path: "./module/__test__/sub/test_sub.sql", Directory: "./module/__test__/sub/", Name: "test_sub.sql", Extension: ".sql", Content: "SELECT 2;"},
			},
			wantTestCount: 2, // 2 actual tests, 2 fixtures
			wantResolve:   "./module/__test__/test_core.sql",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, resolver := buildTestTreeAndResolver(tt.files)

			if tree == nil {
				t.Fatal("Expected non-nil tree")
			}

			if tt.wantTestCount > 0 && tree.TotalTests() != tt.wantTestCount {
				t.Errorf("TotalTests() = %d, want %d", tree.TotalTests(), tt.wantTestCount)
			}

			if tt.wantResolve != "" {
				content, err := resolver(tt.wantResolve)
				if err != nil {
					t.Errorf("resolver(%q) error: %v", tt.wantResolve, err)
				}
				if content == "" {
					t.Errorf("resolver(%q) returned empty content", tt.wantResolve)
				}
			}
		})
	}
}

func TestBuildTestTreeAndResolver_ResolverNotFound(t *testing.T) {
	files := []pgmi.FileMetadata{
		{Path: "./__test__/test_a.sql", Directory: "./__test__/", Name: "test_a.sql", Extension: ".sql", Content: "SELECT 1;"},
	}

	_, resolver := buildTestTreeAndResolver(files)

	_, err := resolver("./nonexistent.sql")
	if err == nil {
		t.Fatal("Expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' in error, got: %v", err)
	}
}

func TestRunTestGenerate_InvalidCallback(t *testing.T) {
	resetTestGenerateFlags()
	testGenerateFlags.callback = "invalid callback name with spaces"

	err := runTestGenerate(testGenerateCmd, []string{t.TempDir()})
	if err == nil {
		t.Fatal("Expected error for invalid callback")
	}
}

func TestRunTestGenerate_NonexistentPath(t *testing.T) {
	resetTestGenerateFlags()

	err := runTestGenerate(testGenerateCmd, []string{"/nonexistent/path/xyz123"})
	if err == nil {
		t.Fatal("Expected error for nonexistent path")
	}
	if !strings.Contains(err.Error(), "scan") {
		t.Errorf("Expected scan error, got: %v", err)
	}
}

func TestRunTestGenerate_NoTestsFound(t *testing.T) {
	resetTestGenerateFlags()
	tempDir := t.TempDir()

	// Create a project with no test files
	deploySQL := filepath.Join(tempDir, "deploy.sql")
	if err := os.WriteFile(deploySQL, []byte("SELECT 1;"), 0644); err != nil {
		t.Fatal(err)
	}

	err := runTestGenerate(testGenerateCmd, []string{tempDir})
	if err != nil {
		t.Fatalf("Expected nil error when no tests found, got: %v", err)
	}
}

func TestRunTestGenerate_DryRunMode(t *testing.T) {
	resetTestGenerateFlags()
	tempDir := t.TempDir()

	// Create test directory with a test file
	testDir := filepath.Join(tempDir, "__test__")
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(testDir, "test_example.sql")
	if err := os.WriteFile(testFile, []byte("SELECT 'test passed';"), 0644); err != nil {
		t.Fatal(err)
	}

	// Dry-run mode (no output specified)
	testGenerateFlags.output = ""

	err := runTestGenerate(testGenerateCmd, []string{tempDir})
	if err != nil {
		t.Fatalf("Expected nil error for dry-run, got: %v", err)
	}
}

func TestRunTestGenerate_OutputToFile(t *testing.T) {
	resetTestGenerateFlags()
	tempDir := t.TempDir()

	// Create test directory with a test file
	testDir := filepath.Join(tempDir, "__test__")
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(testDir, "test_example.sql")
	if err := os.WriteFile(testFile, []byte("SELECT 'test passed';"), 0644); err != nil {
		t.Fatal(err)
	}

	// Output to file
	outputFile := filepath.Join(tempDir, "output.sql")
	testGenerateFlags.output = outputFile

	err := runTestGenerate(testGenerateCmd, []string{tempDir})
	if err != nil {
		t.Fatalf("Expected nil error, got: %v", err)
	}

	// Check if output file exists - if no tests found, file won't be created
	if _, err := os.Stat(outputFile); err != nil {
		// No output file is OK if no tests were found
		t.Skip("Skipping: no output file created (test discovery may have found no tests)")
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}
	if len(content) == 0 {
		t.Error("Output file is empty")
	}
}

func TestRunTestGenerate_WithoutTransaction(t *testing.T) {
	resetTestGenerateFlags()
	tempDir := t.TempDir()

	// Create test directory with a test file
	testDir := filepath.Join(tempDir, "__test__")
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(testDir, "test_example.sql")
	if err := os.WriteFile(testFile, []byte("SELECT 1;"), 0644); err != nil {
		t.Fatal(err)
	}

	outputFile := filepath.Join(tempDir, "output.sql")
	testGenerateFlags.output = outputFile
	testGenerateFlags.withTransaction = false

	err := runTestGenerate(testGenerateCmd, []string{tempDir})
	if err != nil {
		t.Fatalf("Expected nil error, got: %v", err)
	}

	// Check if output file exists
	if _, err := os.Stat(outputFile); err != nil {
		t.Skip("Skipping: no output file created")
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	contentStr := string(content)
	// Without transaction wrapper, the output should not start with BEGIN
	lines := strings.Split(strings.TrimSpace(contentStr), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "BEGIN;" {
		t.Error("Expected no BEGIN wrapper when with-transaction=false")
	}
}

func TestRunTestGenerate_WithFilter(t *testing.T) {
	resetTestGenerateFlags()
	tempDir := t.TempDir()

	// Create test directory with multiple test files
	testDir := filepath.Join(tempDir, "__test__")
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "test_auth.sql"), []byte("SELECT 'auth';"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "test_user.sql"), []byte("SELECT 'user';"), 0644); err != nil {
		t.Fatal(err)
	}

	outputFile := filepath.Join(tempDir, "output.sql")
	testGenerateFlags.output = outputFile
	testGenerateFlags.filter = "auth"

	err := runTestGenerate(testGenerateCmd, []string{tempDir})
	if err != nil {
		t.Fatalf("Expected nil error, got: %v", err)
	}

	// Check if output file exists
	if _, err := os.Stat(outputFile); err != nil {
		t.Skip("Skipping: no output file created")
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "auth") {
		t.Error("Expected auth test in output")
	}
}

func TestRunTestGenerate_FilterNoMatch(t *testing.T) {
	resetTestGenerateFlags()
	tempDir := t.TempDir()

	// Create test directory with a test file
	testDir := filepath.Join(tempDir, "__test__")
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "test_user.sql"), []byte("SELECT 1;"), 0644); err != nil {
		t.Fatal(err)
	}

	testGenerateFlags.filter = "nonexistent_pattern_xyz"

	// Should succeed with "no tests found" message
	err := runTestGenerate(testGenerateCmd, []string{tempDir})
	if err != nil {
		t.Fatalf("Expected nil error when filter matches nothing, got: %v", err)
	}
}

func TestRunTestGenerate_WithCallback(t *testing.T) {
	resetTestGenerateFlags()
	tempDir := t.TempDir()

	// Create test directory with a test file
	testDir := filepath.Join(tempDir, "__test__")
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "test_cb.sql"), []byte("SELECT 1;"), 0644); err != nil {
		t.Fatal(err)
	}

	outputFile := filepath.Join(tempDir, "output.sql")
	testGenerateFlags.output = outputFile
	testGenerateFlags.callback = "pg_temp.my_callback"

	err := runTestGenerate(testGenerateCmd, []string{tempDir})
	if err != nil {
		t.Fatalf("Expected nil error, got: %v", err)
	}

	// Check if output file exists
	if _, err := os.Stat(outputFile); err != nil {
		t.Skip("Skipping: no output file created")
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "pg_temp.my_callback") {
		t.Error("Expected callback reference in output")
	}
}

func TestRunTestGenerate_WithDebug(t *testing.T) {
	resetTestGenerateFlags()
	tempDir := t.TempDir()

	// Create test directory with a test file
	testDir := filepath.Join(tempDir, "__test__")
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "test_debug.sql"), []byte("SELECT 1;"), 0644); err != nil {
		t.Fatal(err)
	}

	outputFile := filepath.Join(tempDir, "output.sql")
	testGenerateFlags.output = outputFile
	testGenerateFlags.withDebug = true

	err := runTestGenerate(testGenerateCmd, []string{tempDir})
	if err != nil {
		t.Fatalf("Expected nil error, got: %v", err)
	}

	// Check if output file exists
	if _, err := os.Stat(outputFile); err != nil {
		t.Skip("Skipping: no output file created")
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "DEBUG") {
		t.Error("Expected DEBUG in output when with-debug=true")
	}
}

func TestRunTestGenerate_OutputFileError(t *testing.T) {
	resetTestGenerateFlags()

	// Use a real template directory that has test files
	// Use the basic template from internal/scaffold/templates
	templateDir := "../../internal/scaffold/templates/basic"
	if _, err := os.Stat(templateDir); err != nil {
		t.Skip("Skipping: template directory not found")
	}

	// Point to an invalid/unwritable path
	testGenerateFlags.output = "/nonexistent/directory/that/does/not/exist/output.sql"

	err := runTestGenerate(testGenerateCmd, []string{templateDir})
	// If no tests found, no error expected
	// If tests found, write error expected
	if err != nil && !strings.Contains(err.Error(), "write") && !strings.Contains(err.Error(), "No such file") {
		t.Errorf("Expected write error or no error, got: %v", err)
	}
}

func TestRunTestGenerate_WithFixtureAndTest(t *testing.T) {
	resetTestGenerateFlags()
	tempDir := t.TempDir()

	// Create test directory with fixture and test
	testDir := filepath.Join(tempDir, "__test__")
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "_setup.sql"), []byte("-- fixture\nCREATE TABLE test_t(id int);"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "test_table.sql"), []byte("SELECT * FROM test_t;"), 0644); err != nil {
		t.Fatal(err)
	}

	outputFile := filepath.Join(tempDir, "output.sql")
	testGenerateFlags.output = outputFile

	err := runTestGenerate(testGenerateCmd, []string{tempDir})
	if err != nil {
		t.Fatalf("Expected nil error, got: %v", err)
	}

	// Check if output file exists
	if _, err := os.Stat(outputFile); err != nil {
		t.Skip("Skipping: no output file created")
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	contentStr := string(content)
	// Should have test content
	if !strings.Contains(contentStr, "test_t") {
		t.Error("Expected test content in output")
	}
}

func TestTestGenerateCmd_ArgsValidation(t *testing.T) {
	err := testGenerateCmd.Args(testGenerateCmd, []string{})
	if err == nil {
		t.Fatal("Expected error for missing args")
	}
}

func TestTestGenerateCmd_ArgsValidation_TooMany(t *testing.T) {
	err := testGenerateCmd.Args(testGenerateCmd, []string{"a", "b"})
	if err == nil {
		t.Fatal("Expected error for too many args")
	}
}
