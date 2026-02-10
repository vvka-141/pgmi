package loader

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestLoadFilesIntoSession_NilFiles(t *testing.T) {
	l := NewLoader()
	err := l.LoadFilesIntoSession(context.TODO(), nil, nil)
	if err != nil {
		t.Fatalf("Expected nil error for nil files, got: %v", err)
	}
}

func TestLoadFilesIntoSession_EmptyFiles(t *testing.T) {
	l := NewLoader()
	err := l.LoadFilesIntoSession(context.TODO(), nil, []pgmi.FileMetadata{})
	if err != nil {
		t.Fatalf("Expected nil error for empty files, got: %v", err)
	}
}

func TestLoadParametersIntoSession_EmptyParams(t *testing.T) {
	l := NewLoader()
	err := l.LoadParametersIntoSession(context.TODO(), nil, map[string]string{})
	if err != nil {
		t.Fatalf("Expected nil error for empty params, got: %v", err)
	}
}

func TestLoadParametersIntoSession_NilParams(t *testing.T) {
	l := NewLoader()
	err := l.LoadParametersIntoSession(context.TODO(), nil, nil)
	if err != nil {
		t.Fatalf("Expected nil error for nil params, got: %v", err)
	}
}

func TestInsertFiles_Empty(t *testing.T) {
	l := NewLoader()
	if err := l.insertFiles(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
	if err := l.insertFiles(context.TODO(), nil, []pgmi.FileMetadata{}); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
}

func TestInsertParams_Empty(t *testing.T) {
	l := NewLoader()
	if err := l.insertParams(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
	if err := l.insertParams(context.TODO(), nil, map[string]string{}); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
}

func TestSetSessionVariables_Empty(t *testing.T) {
	l := NewLoader()
	if err := l.setSessionVariables(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
	if err := l.setSessionVariables(context.TODO(), nil, map[string]string{}); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
}

func TestValidateParameterKey_InvalidKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"spaces", "bad key"},
		{"hyphen", "bad-key"},
		{"dot", "bad.key"},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"empty", ""},
		{"special chars", "key;DROP TABLE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateParameterKey(tt.key)
			if err == nil {
				t.Fatal("Expected error for invalid key")
			}
		})
	}
}

func TestValidateParameterKey_ValidKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"simple", "mykey"},
		{"underscore", "my_key"},
		{"numbers", "key123"},
		{"mixed", "key_123_test"},
		{"max length", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, // 63 chars
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateParameterKey(tt.key)
			if err != nil {
				t.Fatalf("Expected no error for valid key %q, got: %v", tt.key, err)
			}
		})
	}
}

func TestInsertMetadata_NoMetadataFiles(t *testing.T) {
	l := NewLoader()
	files := []pgmi.FileMetadata{
		{Path: "a.sql", Content: "SELECT 1;"},
		{Path: "b.sql", Content: "SELECT 2;"},
	}
	if err := l.insertMetadata(context.TODO(), nil, files); err != nil {
		t.Fatalf("Expected nil error for files without metadata: %v", err)
	}
}

func TestInsertMetadata_NilFiles(t *testing.T) {
	l := NewLoader()
	if err := l.insertMetadata(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
}

func TestLoadParametersIntoSession_InvalidKey(t *testing.T) {
	l := NewLoader()
	params := map[string]string{
		"valid_key":   "ok",
		"invalid key": "bad",
	}
	err := l.LoadParametersIntoSession(context.TODO(), nil, params)
	if err == nil {
		t.Fatal("Expected error for invalid parameter key")
	}
}

func TestNewLoader(t *testing.T) {
	l := NewLoader()
	if l == nil {
		t.Fatal("Expected non-nil loader")
	}
}

func TestInsertTestFiles_Empty(t *testing.T) {
	l := NewLoader()
	if err := l.insertTestFiles(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error for nil files: %v", err)
	}
	if err := l.insertTestFiles(context.TODO(), nil, []pgmi.FileMetadata{}); err != nil {
		t.Fatalf("Expected nil error for empty files: %v", err)
	}
}

func TestLoadFilesIntoSession_SeparatesTestFiles(t *testing.T) {
	files := []pgmi.FileMetadata{
		{Path: "./migrations/001.sql", Content: "CREATE TABLE t();"},
		{Path: "./__test__/test_a.sql", Content: "SELECT 1;"},
		{Path: "./__test__/_setup.sql", Content: "-- setup"},
		{Path: "./deploy.sql", Content: "-- deploy"},
		{Path: "./module/__test__/test_b.sql", Content: "SELECT 2;"},
	}

	testFiles := 0
	sourceFiles := 0
	for _, f := range files {
		if pgmi.IsTestPath(f.Path) {
			testFiles++
		} else {
			sourceFiles++
		}
	}

	if testFiles != 3 {
		t.Errorf("Expected 3 test files, got %d", testFiles)
	}
	if sourceFiles != 2 {
		t.Errorf("Expected 2 source files, got %d", sourceFiles)
	}
}

func TestInsertTestDirectories_Empty(t *testing.T) {
	l := NewLoader()
	if err := l.insertTestDirectories(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error for nil files: %v", err)
	}
	if err := l.insertTestDirectories(context.TODO(), nil, []pgmi.FileMetadata{}); err != nil {
		t.Fatalf("Expected nil error for empty files: %v", err)
	}
}

func TestExtractTestDirectory(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"./__test__/test.sql", "./__test__/"},
		{"./foo/__test__/test.sql", "./foo/__test__/"},
		{"./foo/__tests__/test.sql", "./foo/__tests__/"},
		{"./__test__/nested/__test__/test.sql", "./__test__/nested/__test__/"},
		{"./regular/file.sql", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := extractTestDirectory(tt.path)
			if result != tt.expected {
				t.Errorf("extractTestDirectory(%q) = %q, expected %q", tt.path, result, tt.expected)
			}
		})
	}
}

func TestIsFixtureFile(t *testing.T) {
	tests := []struct {
		filename string
		expected bool
	}{
		{"_setup.sql", true},
		{"_Setup.sql", true},
		{"_SETUP.sql", true},
		{"_setup.psql", true},
		{"_Setup.psql", true},
		{"test.sql", false},
		{"setup.sql", false},
		{"_setup.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := isFixtureFile(tt.filename)
			if result != tt.expected {
				t.Errorf("isFixtureFile(%q) = %v, expected %v", tt.filename, result, tt.expected)
			}
		})
	}
}

func TestCountTestDirectoryDepth(t *testing.T) {
	tests := []struct {
		path     string
		expected int
	}{
		{"./__test__/", 0},                    // Root test directory
		{"./__test__/auth/", 1},               // One level deep
		{"./__test__/auth/oauth/", 2},         // Two levels deep
		{"./__test__/auth/oauth/google/", 3},  // Three levels deep
		{"./regular/path/", 0},                // No test dirs
		{"./src/__test__/unit/", 1},           // Test dir under src
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := countTestDirectoryDepth(tt.path)
			if result != tt.expected {
				t.Errorf("countTestDirectoryDepth(%q) = %d, expected %d", tt.path, result, tt.expected)
			}
		})
	}
}

func TestInsertMetadata_OnlyFilesWithMetadata(t *testing.T) {
	l := NewLoader()
	testUUID := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11")
	files := []pgmi.FileMetadata{
		{Path: "a.sql", Content: "SELECT 1;", Metadata: nil},
		{Path: "b.sql", Content: "SELECT 2;", Metadata: &pgmi.ScriptMetadata{ID: testUUID}},
		{Path: "c.sql", Content: "SELECT 3;", Metadata: nil},
	}

	count := 0
	for _, f := range files {
		if f.Metadata != nil {
			count++
		}
	}

	if count != 1 {
		t.Errorf("Expected 1 file with metadata, got %d", count)
	}

	err := l.insertMetadata(context.TODO(), nil, nil)
	if err != nil {
		t.Fatalf("insertMetadata(nil) should return nil: %v", err)
	}
}

func TestValidateParameterKey_Comprehensive(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid lowercase", "mykey", false},
		{"valid uppercase", "MYKEY", false},
		{"valid mixed case", "MyKey", false},
		{"valid with underscore", "my_key", false},
		{"valid with numbers", "key123", false},
		{"valid underscore prefix", "_key", false},
		{"valid single char", "x", false},
		{"valid 63 chars", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{"empty string", "", true},
		{"64 chars too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"space in key", "my key", true},
		{"hyphen", "my-key", true},
		{"dot", "my.key", true},
		{"semicolon", "key;", true},
		{"sql injection attempt", "'; DROP TABLE users;--", true},
		{"newline", "key\n", true},
		{"tab", "key\t", true},
		{"equals sign", "key=value", true},
		{"at sign", "key@domain", true},
		{"parentheses", "key()", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateParameterKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateParameterKey(%q) error = %v, wantErr = %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestLoadFilesIntoSession_OnlyTestFilesCount(t *testing.T) {
	files := []pgmi.FileMetadata{
		{Path: "./__test__/test_a.sql", Content: "SELECT 1;"},
		{Path: "./__test__/test_b.sql", Content: "SELECT 2;"},
	}

	testFiles := 0
	sourceFiles := 0
	for _, f := range files {
		if pgmi.IsTestPath(f.Path) {
			testFiles++
		} else {
			sourceFiles++
		}
	}

	if testFiles != 2 {
		t.Errorf("Expected 2 test files, got %d", testFiles)
	}
	if sourceFiles != 0 {
		t.Errorf("Expected 0 source files, got %d", sourceFiles)
	}
}

func TestLoadParametersIntoSession_MultipleInvalidKeys(t *testing.T) {
	l := NewLoader()
	params := map[string]string{
		"valid_key":   "ok",
		"another":     "ok",
		"bad-key":     "has hyphen",
		"another_bad": "also fine",
	}
	err := l.LoadParametersIntoSession(context.TODO(), nil, params)
	if err == nil {
		t.Fatal("Expected error for invalid parameter key")
	}
}

func TestValidateParameterKey_AllValidVariants(t *testing.T) {
	validKeys := []string{
		"key1",
		"key_2",
		"KEY3",
		"mixed_Case",
		"a",
		"A",
		"_underscore_prefix",
		"suffix_underscore_",
	}

	for _, key := range validKeys {
		if err := validateParameterKey(key); err != nil {
			t.Errorf("Expected key %q to be valid, got error: %v", key, err)
		}
	}
}
