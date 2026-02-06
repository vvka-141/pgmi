package scanner

import (
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
		if isSQLExtension(f.Extension) {
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
	s, _ := newTestScanner()

	if err := s.ValidateDeploySQL("/project"); err == nil {
		t.Error("Expected error for missing deploy.sql")
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
