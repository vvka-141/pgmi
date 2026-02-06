package testdiscovery

import (
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestConvertFromFileMetadata(t *testing.T) {
	files := []pgmi.FileMetadata{
		{
			Path:      "./users/schema.sql",
			Name:      "schema.sql",
			Directory: "users",
			Extension: ".sql",
			Content:   "CREATE TABLE users();",
		},
		{
			Path:      "./users/__test__/01_test.sql",
			Name:      "01_test.sql",
			Directory: "users/__test__",
			Extension: ".sql",
			Content:   "SELECT 1;",
		},
		{
			Path:      "./readme.md",
			Name:      "readme.md",
			Directory: "",
			Extension: ".md",
			Content:   "# Readme",
		},
	}

	sources := ConvertFromFileMetadata(files)

	if len(sources) != 3 {
		t.Fatalf("Expected 3 sources, got %d", len(sources))
	}

	// Check first source (non-test SQL)
	if sources[0].Path != "./users/schema.sql" {
		t.Errorf("sources[0].Path = %q", sources[0].Path)
	}
	if sources[0].Directory != "users/" {
		t.Errorf("sources[0].Directory = %q, expected 'users/'", sources[0].Directory)
	}
	if !sources[0].IsSQLFile {
		t.Error("sources[0] should be SQL file")
	}
	if sources[0].IsTestFile {
		t.Error("sources[0] should NOT be test file")
	}

	// Check second source (test SQL)
	if sources[1].Path != "./users/__test__/01_test.sql" {
		t.Errorf("sources[1].Path = %q", sources[1].Path)
	}
	if sources[1].Directory != "users/__test__/" {
		t.Errorf("sources[1].Directory = %q, expected 'users/__test__/'", sources[1].Directory)
	}
	if !sources[1].IsSQLFile {
		t.Error("sources[1] should be SQL file")
	}
	if !sources[1].IsTestFile {
		t.Error("sources[1] should be test file")
	}

	// Check third source (non-SQL)
	if sources[2].IsSQLFile {
		t.Error("sources[2] should NOT be SQL file")
	}
}

func TestEnsureTrailingSlash(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "./"},
		{".", "./"},
		{"users", "users/"},
		{"users/", "users/"},
		{"users/__test__", "users/__test__/"},
		{"users/__test__/", "users/__test__/"},
	}

	for _, tc := range tests {
		result := ensureTrailingSlash(tc.input)
		if result != tc.expected {
			t.Errorf("ensureTrailingSlash(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

func TestIsSQLExtension(t *testing.T) {
	sqlExtensions := []string{".sql", ".SQL", ".ddl", ".dml", ".dql", ".dcl", ".psql", ".pgsql", ".plpgsql"}
	nonSQLExtensions := []string{".md", ".txt", ".go", ".js", ""}

	for _, ext := range sqlExtensions {
		if !isSQLExtension(ext) {
			t.Errorf("%q should be SQL extension", ext)
		}
	}

	for _, ext := range nonSQLExtensions {
		if isSQLExtension(ext) {
			t.Errorf("%q should NOT be SQL extension", ext)
		}
	}
}
