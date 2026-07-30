package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func newTestScanner() (*Scanner, *filesystem.MemoryFileSystem) {
	fs := filesystem.NewMemoryFileSystem("/project")
	return NewScannerWithFS(checksum.New(), fs), fs
}

func TestScanDirectory_RejectsNonTextFiles(t *testing.T) {
	t.Run("NUL byte", func(t *testing.T) {
		s, fs := newTestScanner()
		fs.AddFile("deploy.sql", "SELECT 1;")
		fs.AddFile("data/blob.bin", "ok\x00binary")
		_, err := s.ScanDirectory("/project")
		if err == nil || !strings.Contains(err.Error(), "NUL byte") {
			t.Fatalf("expected NUL-byte error naming the file, got: %v", err)
		}
		if !strings.Contains(err.Error(), "blob.bin") {
			t.Fatalf("error should name the offending file, got: %v", err)
		}
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		s, fs := newTestScanner()
		fs.AddFile("deploy.sql", "SELECT 1;")
		fs.AddFile("data/bad.txt", "valid then \xff\xfe invalid")
		_, err := s.ScanDirectory("/project")
		if err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
			t.Fatalf("expected UTF-8 error, got: %v", err)
		}
	})
}

// Three layers used to wrap the same file path, one of them in OS separators:
// "failed to process file migrations\001.sql: metadata error in
// ./migrations/001.sql: invalid PGMI metadata in ./migrations/001.sql:".
// processFile names the file; nobody above it may name it again.
func TestScanDirectory_ErrorsNameTheFileExactlyOnce(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
	}{
		{
			name:    "invalid metadata",
			path:    "migrations/001_users.sql",
			content: "/*\n<pgmi-meta idempotent=\"false\">\n</pgmi-meta>\n*/\nSELECT 1;",
		},
		{
			name:    "malformed metadata XML",
			path:    "migrations/002_orders.sql",
			content: "/*\n<pgmi-meta id=\"550e8400-e29b-41d4-a716-446655440000\" idempotent=\"false\">\n  <description>unclosed\n</pgmi-meta>\n*/\nSELECT 1;",
		},
		{
			name:    "NUL byte",
			path:    "data/blob.bin",
			content: "ok\x00binary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, fs := newTestScanner()
			fs.AddFile("deploy.sql", "SELECT 1;")
			fs.AddFile(tt.path, tt.content)

			_, err := s.ScanDirectory("/project")
			if err == nil {
				t.Fatal("expected an error")
			}

			// Counted without the ./ prefix so that a wrapper adding a bare
			// or OS-separator path is caught too, not just a second ./ copy.
			if got := strings.Count(err.Error(), tt.path); got != 1 {
				t.Errorf("file named %d times, want exactly 1:\n%v", got, err)
			}
			if !strings.Contains(err.Error(), "./"+tt.path) {
				t.Errorf("the single mention should be in ./ form:\n%v", err)
			}
		})
	}
}

// A scaffolded project is almost always a git repository, and .git/index is
// binary — scanning it made `pgmi deploy .` fail in every real project.
func TestScanDirectory_SkipsHiddenAndToolingDirectories(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("migrations/001_users.sql", "CREATE TABLE users (id int);")
	fs.AddFile("__test__/test_users.sql", "SELECT 1;")
	fs.AddFile(".git/index", "DIRC\x00\x00\x00\x02binary")
	fs.AddFile(".venv/lib/site.py", "import sys")
	fs.AddFile("node_modules/pkg/index.js", "module.exports = {};")
	fs.AddFile("api/__pycache__/gen.cpython-312.pyc", "\x03\xf3\r\n")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("scanning a project containing .git must succeed, got: %v", err)
	}

	loaded := make(map[string]bool, len(result.Files))
	for _, f := range result.Files {
		loaded[f.Path] = true
	}

	for _, skipped := range []string{
		"./.git/index",
		"./.venv/lib/site.py",
		"./node_modules/pkg/index.js",
		"./api/__pycache__/gen.cpython-312.pyc",
	} {
		if loaded[skipped] {
			t.Errorf("%s should not be loaded", skipped)
		}
	}

	// pgmi's own dunder directories are not tooling artifacts.
	for _, kept := range []string{"./migrations/001_users.sql", "./__test__/test_users.sql"} {
		if !loaded[kept] {
			t.Errorf("%s should still be loaded, got files: %v", kept, result.Files)
		}
	}
}

func TestNewScanner_NilCalculator(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for nil calculator")
		}
	}()
	NewScanner(nil)
}

func TestNewScannerWithFS_NilArgs(t *testing.T) {
	calc := checksum.New()
	fs := filesystem.NewMemoryFileSystem("/")

	tests := []struct {
		name string
		fn   func()
	}{
		{"nil calculator", func() { NewScannerWithFS(nil, fs) }},
		{"nil filesystem", func() { NewScannerWithFS(calc, nil) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Error("Expected panic")
				}
			}()
			tt.fn()
		})
	}
}

func TestScanDirectory(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("migrations/001_users.sql", "CREATE TABLE users (id int);")
	fs.AddFile("migrations/002_orders.sql", "CREATE TABLE orders (id int);")
	fs.AddFile("config.yaml", "env: dev")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("ScanDirectory failed: %v", err)
	}

	if len(result.Files) != 3 {
		t.Fatalf("Expected 3 files (deploy.sql excluded), got %d", len(result.Files))
	}

	for _, f := range result.Files {
		if strings.ToLower(f.Name) == "deploy.sql" {
			t.Error("deploy.sql should be excluded")
		}
		if !strings.HasPrefix(f.Path, "./") {
			t.Errorf("Path should have ./ prefix, got %q", f.Path)
		}
		if strings.Contains(f.Path, "\\") {
			t.Errorf("Path should use forward slashes, got %q", f.Path)
		}
		if f.Checksum == "" || f.ChecksumRaw == "" {
			t.Errorf("Checksums should be populated for %s", f.Path)
		}
		if f.Content == "" {
			t.Errorf("Content should be populated for %s", f.Path)
		}
	}
}

func TestScanDirectory_NestedDirectories(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("root.sql", "SELECT 1;")
	fs.AddFile("level1/a.sql", "SELECT 1;")
	fs.AddFile("level1/level2/b.sql", "SELECT 1;")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("ScanDirectory failed: %v", err)
	}

	depthByName := map[string]int{}
	for _, f := range result.Files {
		depthByName[f.Name] = f.Depth
	}

	expected := map[string]int{"root.sql": 0, "a.sql": 1, "b.sql": 2}
	for name, wantDepth := range expected {
		if got, ok := depthByName[name]; !ok {
			t.Errorf("File %s not found", name)
		} else if got != wantDepth {
			t.Errorf("File %s: depth=%d, want %d", name, got, wantDepth)
		}
	}
}

func TestScanDirectory_EmptyDirectory(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("ScanDirectory failed: %v", err)
	}

	if len(result.Files) != 0 {
		t.Errorf("Expected 0 files, got %d", len(result.Files))
	}
}

func TestScanDirectory_NonexistentPath(t *testing.T) {
	s, _ := newTestScanner()

	_, err := s.ScanDirectory("/nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent path")
	}
}

func TestScanDirectory_TestFiles(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("schema/functions.sql", "CREATE FUNCTION f();")
	fs.AddFile("schema/__test__/test_f.sql", "SELECT f();")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("ScanDirectory failed: %v", err)
	}

	if len(result.Files) != 2 {
		t.Fatalf("Expected 2 files, got %d", len(result.Files))
	}

	var testFile *pgmi.FileMetadata
	for i, f := range result.Files {
		if strings.Contains(f.Path, "__test__") {
			testFile = &result.Files[i]
		}
	}

	if testFile == nil {
		t.Fatal("Test file not found in results")
	}

	if !pgmi.IsTestPath(testFile.Path) {
		t.Error("IsTestPath should return true for __test__ file")
	}

	if testFile.Metadata != nil {
		t.Error("Test files should not have metadata extracted")
	}
}

func TestScanDirectory_RootLevelTestFiles(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("migrations/001_users.sql", "CREATE TABLE users();")
	fs.AddFile("__test__/00_fixture.sql", "INSERT INTO users VALUES (1);")
	fs.AddFile("__test__/test_users.sql", "SELECT * FROM users;")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("ScanDirectory failed: %v", err)
	}

	if len(result.Files) != 3 {
		t.Fatalf("Expected 3 files, got %d", len(result.Files))
	}

	testFileCount := 0
	for _, f := range result.Files {
		if pgmi.IsTestPath(f.Path) {
			testFileCount++
			if !strings.HasPrefix(f.Path, "./__test__/") {
				t.Errorf("Root-level test file should have path ./__test__/*, got %q", f.Path)
			}
			if f.Metadata != nil {
				t.Errorf("Test file %s should not have metadata extracted", f.Path)
			}
		}
	}

	if testFileCount != 2 {
		t.Errorf("Expected 2 test files detected by IsTestPath, got %d", testFileCount)
	}
}

func TestScanDirectory_SQLExtensions(t *testing.T) {
	extensions := []string{".sql", ".ddl", ".dml", ".dql", ".dcl", ".psql", ".pgsql", ".plpgsql"}

	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	for _, ext := range extensions {
		fs.AddFile("file"+ext, "SELECT 1;")
	}
	fs.AddFile("readme.md", "# Readme")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("ScanDirectory failed: %v", err)
	}

	sqlCount := 0
	for _, f := range result.Files {
		if pgmi.IsSQLExtension(f.Extension) {
			sqlCount++
		}
	}

	if sqlCount != len(extensions) {
		t.Errorf("Expected %d SQL files, got %d", len(extensions), sqlCount)
	}
}

func TestValidateDeploySQL(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")

	if err := s.ValidateDeploySQL("/project"); err != nil {
		t.Errorf("ValidateDeploySQL failed: %v", err)
	}
}

func TestValidateDeploySQL_Missing(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("other.sql", "SELECT 1;")

	err := s.ValidateDeploySQL("/project")
	if err == nil {
		t.Fatal("Expected error for missing deploy.sql")
	}
	if !errors.Is(err, pgmi.ErrDeploySQLNotFound) {
		t.Errorf("Expected errors.Is(err, ErrDeploySQLNotFound), got: %v", err)
	}
	if code := pgmi.ExitCodeForError(err); code != pgmi.ExitDeploySQLMissing {
		t.Errorf("Expected exit code %d (ExitDeploySQLMissing), got %d", pgmi.ExitDeploySQLMissing, code)
	}
}

func TestValidateDeploySQL_NonexistentPath(t *testing.T) {
	s, _ := newTestScanner()

	err := s.ValidateDeploySQL("/no-such-dir")
	if err == nil {
		t.Fatal("Expected error for nonexistent path")
	}
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Errorf("Expected ErrInvalidConfig (exit 10), got: %v", err)
	}
	if !strings.Contains(err.Error(), "/no-such-dir") {
		t.Errorf("Error should name the path, got: %v", err)
	}
}

func TestValidateDeploySQL_FileAsProject(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")

	err := s.ValidateDeploySQL("/project/deploy.sql")
	if err == nil {
		t.Fatal("Expected error when project path is a file")
	}
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Errorf("Expected ErrInvalidConfig (exit 10), got: %v", err)
	}
	if !strings.Contains(err.Error(), "is a file") {
		t.Errorf("Error should say 'is a file', got: %v", err)
	}
}

func TestValidateDeploySQL_IsDirectory(t *testing.T) {
	s, fs := newTestScanner()
	// Add a file inside a "deploy.sql" directory to make that path exist as a directory
	fs.AddFile("deploy.sql/inner.sql", "SELECT 1;")

	err := s.ValidateDeploySQL("/project")
	if err == nil {
		t.Error("Expected error when deploy.sql is a directory")
	}
	if err != nil && !strings.Contains(err.Error(), "directory") {
		t.Errorf("Error should mention 'directory', got: %v", err)
	}
}

func TestReadDeploySQL(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")

	content, err := s.ReadDeploySQL("/project")
	if err != nil {
		t.Fatalf("ReadDeploySQL failed: %v", err)
	}

	if content != "SELECT 1;" {
		t.Errorf("Unexpected content: %q", content)
	}
}

func TestReadDeploySQL_Missing(t *testing.T) {
	s, _ := newTestScanner()

	_, err := s.ReadDeploySQL("/project")
	if err == nil {
		t.Error("Expected error for missing deploy.sql")
	}
}

func TestReadDeploySQL_StripsUTF8BOM(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "\xef\xbb\xbfSELECT 1;")

	content, err := s.ReadDeploySQL("/project")
	if err != nil {
		t.Fatalf("ReadDeploySQL failed: %v", err)
	}
	if content != "SELECT 1;" {
		t.Errorf("BOM not stripped: got %q, want %q", content, "SELECT 1;")
	}
}

// A BOM reaches PostgreSQL glued to the first keyword, and the deploy fails
// with a syntax error naming a keyword that reads as correct. Measured against
// PG 17.10 before the fix: `syntax error at or near "CREATE" (SQLSTATE 42601)`
// on a file whose first line was a valid CREATE TABLE.
func TestScanDirectory_StripsUTF8BOMFromProjectFiles(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("migrations/001_bom.sql", "\xef\xbb\xbfCREATE TABLE t (id int);")
	fs.AddFile("migrations/002_plain.sql", "CREATE TABLE t (id int);")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("ScanDirectory failed: %v", err)
	}

	byPath := make(map[string]pgmi.FileMetadata, len(result.Files))
	for _, f := range result.Files {
		byPath[f.Path] = f
	}

	bom, ok := byPath["./migrations/001_bom.sql"]
	if !ok {
		t.Fatalf("BOM file missing from scan: %v", byPath)
	}
	if bom.Content != "CREATE TABLE t (id int);" {
		t.Errorf("BOM not stripped from content: got %q", bom.Content)
	}

	// Identical scripts must keep one identity, so that saving a file in an
	// editor that adds a BOM does not read as a changed script.
	plain := byPath["./migrations/002_plain.sql"]
	if bom.ChecksumRaw != plain.ChecksumRaw {
		t.Errorf("BOM changed the raw checksum: %q vs %q", bom.ChecksumRaw, plain.ChecksumRaw)
	}
}

func TestReadDeploySQL_RejectsInvalidUTF8(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "-- caf\xe9 header\nSELECT 1;")

	_, err := s.ReadDeploySQL("/project")
	if err == nil {
		t.Fatal("expected error for Latin-1 byte in deploy.sql")
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("error should mention UTF-8, got: %v", err)
	}
	if !strings.Contains(err.Error(), "offset") {
		t.Errorf("error should name the byte offset, got: %v", err)
	}
}

// A project path that does not exist, or that is a file, is invalid
// configuration and exits 10. The deploy path always reported it that way via
// ValidateDeploySQL, but `pgmi metadata plan|validate|scaffold` call
// ScanDirectory directly and so exited 1 — an undifferentiated failure for a
// plain typo in the argument.
func TestScanDirectory_BadProjectPathIsInvalidConfig(t *testing.T) {
	s := NewScanner(checksum.New())

	fileInsteadOfDir := filepath.Join(t.TempDir(), "a.sql")
	if err := os.WriteFile(fileInsteadOfDir, []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, tc := range []struct{ name, path string }{
		{"path does not exist", filepath.Join(t.TempDir(), "no-such-directory")},
		{"path is a file", fileInsteadOfDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ScanDirectory(tc.path)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, pgmi.ErrInvalidConfig) {
				t.Errorf("not an ErrInvalidConfig chain, so this exits 1 rather than 10: %v", err)
			}
			if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
				t.Errorf("exit code %d, want %d", got, pgmi.ExitConfigError)
			}
		})
	}
}
