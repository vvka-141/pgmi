package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsDirectoryEmpty tests the directory emptiness validation
func TestIsDirectoryEmpty(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(t *testing.T) string // Returns path to test
		expectedEmpty bool
		expectedError bool
	}{
		{
			name: "nonexistent directory",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			expectedEmpty: true,
			expectedError: false,
		},
		{
			name: "empty directory",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "empty")
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
				return dir
			},
			expectedEmpty: true,
			expectedError: false,
		},
		{
			name: "directory with file",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "withfile")
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
				testFile := filepath.Join(dir, "test.txt")
				if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				return dir
			},
			expectedEmpty: false,
			expectedError: false,
		},
		{
			name: "directory with subdirectory",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "withsubdir")
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
				subdir := filepath.Join(dir, "subdir")
				if err := os.Mkdir(subdir, 0755); err != nil {
					t.Fatalf("Failed to create subdirectory: %v", err)
				}
				return dir
			},
			expectedEmpty: false,
			expectedError: false,
		},
		{
			name: "directory with hidden file",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "withhidden")
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
				hiddenFile := filepath.Join(dir, ".hidden")
				if err := os.WriteFile(hiddenFile, []byte("content"), 0644); err != nil {
					t.Fatalf("Failed to create hidden file: %v", err)
				}
				return dir
			},
			expectedEmpty: false,
			expectedError: false,
		},
		{
			name: "directory with only pgmi.yaml",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "pgmionly")
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte("connection:\n  host: localhost"), 0644); err != nil {
					t.Fatalf("Failed to create pgmi.yaml: %v", err)
				}
				return dir
			},
			expectedEmpty: true,
			expectedError: false,
		},
		{
			name: "directory with pgmi.yaml and .env",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "managed")
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte("{}"), 0644); err != nil {
					t.Fatalf("Failed to create pgmi.yaml: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("KEY=val"), 0644); err != nil {
					t.Fatalf("Failed to create .env: %v", err)
				}
				return dir
			},
			expectedEmpty: true,
			expectedError: false,
		},
		{
			name: "directory with pgmi.yaml and other files",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "mixed")
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte("{}"), 0644); err != nil {
					t.Fatalf("Failed to create pgmi.yaml: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("data"), 0644); err != nil {
					t.Fatalf("Failed to create other file: %v", err)
				}
				return dir
			},
			expectedEmpty: false,
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			isEmpty, err := isDirectoryEmpty(path)

			if tt.expectedError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectedError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if isEmpty != tt.expectedEmpty {
				t.Errorf("Expected isEmpty=%v, got %v", tt.expectedEmpty, isEmpty)
			}
		})
	}
}

// TestCreateProject_RefusesNonEmptyDirectory tests that CreateProject refuses non-empty directories
func TestCreateProject_RefusesNonEmptyDirectory(t *testing.T) {
	// Create a non-empty directory
	targetDir := filepath.Join(t.TempDir(), "nonempty")
	if err := os.Mkdir(targetDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Add a file to make it non-empty
	existingFile := filepath.Join(targetDir, "existing.txt")
	if err := os.WriteFile(existingFile, []byte("existing content"), 0644); err != nil {
		t.Fatalf("Failed to create existing file: %v", err)
	}

	// Attempt to create project in non-empty directory
	scaffolder := NewScaffolder(false)
	err := scaffolder.CreateProject("testproject", "basic", targetDir)

	// Should return an error
	if err == nil {
		t.Fatal("Expected error when creating project in non-empty directory, got nil")
	}

	// Error message should be clear and helpful
	errMsg := err.Error()
	if !strings.Contains(errMsg, "not empty") {
		t.Errorf("Error message should mention 'not empty', got: %s", errMsg)
	}
}

// TestCreateProject_AcceptsEmptyDirectory tests that CreateProject works with empty directories
func TestCreateProject_AcceptsEmptyDirectory(t *testing.T) {
	// Create an empty directory
	targetDir := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(targetDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Create project in empty directory
	scaffolder := NewScaffolder(false)
	err := scaffolder.CreateProject("testproject", "basic", targetDir)

	if err != nil {
		t.Fatalf("Expected no error for empty directory, got: %v", err)
	}

	// Verify files were created
	deployFile := filepath.Join(targetDir, "deploy.sql")
	if _, err := os.Stat(deployFile); os.IsNotExist(err) {
		t.Error("Expected deploy.sql to be created")
	}
}

// TestCreateProject_AcceptsNonexistentDirectory tests that CreateProject creates and initializes nonexistent directories
func TestCreateProject_AcceptsNonexistentDirectory(t *testing.T) {
	// Use a path that doesn't exist
	targetDir := filepath.Join(t.TempDir(), "newproject")

	// Create project in nonexistent directory
	scaffolder := NewScaffolder(false)
	err := scaffolder.CreateProject("testproject", "basic", targetDir)

	if err != nil {
		t.Fatalf("Expected no error for nonexistent directory, got: %v", err)
	}

	// Verify directory and files were created
	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		t.Error("Expected directory to be created")
	}

	deployFile := filepath.Join(targetDir, "deploy.sql")
	if _, err := os.Stat(deployFile); os.IsNotExist(err) {
		t.Error("Expected deploy.sql to be created")
	}
}

// TestBuildFileTree tests the file tree generation for display
func TestBuildFileTree(t *testing.T) {
	// Create a test directory structure
	rootDir := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(rootDir, 0755); err != nil {
		t.Fatalf("Failed to create root dir: %v", err)
	}

	// Create files and directories
	if err := os.WriteFile(filepath.Join(rootDir, "deploy.sql"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "README.md"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootDir, "migrations"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "migrations", "001_test.sql"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootDir, "__test__"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "__test__", "test_foo.sql"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Build file tree
	tree, err := BuildFileTree(rootDir)
	if err != nil {
		t.Fatalf("Failed to build file tree: %v", err)
	}

	// Verify tree contains expected elements
	expectedElements := []string{
		"deploy.sql",
		"README.md",
		"migrations/",
		"001_test.sql",
		"__test__/",
		"test_foo.sql",
	}

	for _, elem := range expectedElements {
		if !strings.Contains(tree, elem) {
			t.Errorf("Expected tree to contain '%s', got:\n%s", elem, tree)
		}
	}

	// Verify tree uses proper formatting characters
	hasTreeChars := strings.Contains(tree, "├──") || strings.Contains(tree, "└──")
	if !hasTreeChars {
		t.Errorf("Expected tree to use tree formatting characters (├──, └──), got:\n%s", tree)
	}
}

// TestBuildFileTree_EmptyDirectory tests file tree generation for empty directory
func TestBuildFileTree_EmptyDirectory(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(rootDir, 0755); err != nil {
		t.Fatalf("Failed to create root dir: %v", err)
	}

	tree, err := BuildFileTree(rootDir)
	if err != nil {
		t.Fatalf("Failed to build file tree: %v", err)
	}

	// Should return minimal output for empty directory
	if tree == "" {
		t.Error("Expected some output for empty directory")
	}
}
