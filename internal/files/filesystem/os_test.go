package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestOSFileSystem_Open_ValidDirectory(t *testing.T) {
	dir := t.TempDir()
	fs := NewOSFileSystem()

	d, err := fs.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", dir, err)
	}

	absDir, _ := filepath.Abs(dir)
	if d.Path() != absDir {
		t.Errorf("directory.Path() = %q, want %q", d.Path(), absDir)
	}
}

func TestOSFileSystem_Open_NonexistentPath(t *testing.T) {
	fs := NewOSFileSystem()

	_, err := fs.Open(filepath.Join(t.TempDir(), "nonexistent"))
	if err == nil {
		t.Error("Open(nonexistent) should return error")
	}
}

func TestOSFileSystem_Open_FileNotDirectory(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "file.txt")
	os.WriteFile(filePath, []byte("content"), 0644)

	fs := NewOSFileSystem()

	_, err := fs.Open(filePath)
	if err == nil {
		t.Error("Open(file) should return error")
	}
}

func TestOSFileSystem_ReadFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.sql")
	expected := "SELECT 1;"
	os.WriteFile(filePath, []byte(expected), 0644)

	fs := NewOSFileSystem()

	data, err := fs.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != expected {
		t.Errorf("ReadFile() = %q, want %q", string(data), expected)
	}
}

func TestOSFileSystem_ReadFile_Nonexistent(t *testing.T) {
	fs := NewOSFileSystem()

	_, err := fs.ReadFile(filepath.Join(t.TempDir(), "nope.sql"))
	if err == nil {
		t.Error("ReadFile(nonexistent) should return error")
	}
}

func TestOSFileSystem_Stat_File(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.sql")
	os.WriteFile(filePath, []byte("SELECT 1;"), 0644)

	fs := NewOSFileSystem()

	info, err := fs.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.IsDir() {
		t.Error("Stat(file) should not be a directory")
	}
	if info.Name() != "test.sql" {
		t.Errorf("Stat().Name() = %q, want %q", info.Name(), "test.sql")
	}
}

func TestOSFileSystem_Stat_Directory(t *testing.T) {
	dir := t.TempDir()
	fs := NewOSFileSystem()

	info, err := fs.Stat(dir)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Error("Stat(dir) should be a directory")
	}
}

func TestOSFileSystem_Stat_Nonexistent(t *testing.T) {
	fs := NewOSFileSystem()

	_, err := fs.Stat(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Error("Stat(nonexistent) should return error")
	}
}

func TestOSFileSystem_Walk(t *testing.T) {
	dir := t.TempDir()

	// Create a tree:
	//   dir/
	//     a.sql
	//     sub/
	//       b.sql
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0755)
	os.WriteFile(filepath.Join(dir, "a.sql"), []byte("SELECT 1;"), 0644)
	os.WriteFile(filepath.Join(sub, "b.sql"), []byte("SELECT 2;"), 0644)

	fs := NewOSFileSystem()
	d, err := fs.Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	var files []string
	err = d.Walk(func(f File, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !f.Info().IsDir() {
			files = append(files, f.RelativePath())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("Walk found %d files, want 2: %v", len(files), files)
	}

	// Verify relative paths use OS separator
	found := map[string]bool{}
	for _, f := range files {
		found[filepath.ToSlash(f)] = true
	}

	if !found["a.sql"] {
		t.Error("Walk did not find a.sql")
	}
	if !found["sub/b.sql"] {
		t.Error("Walk did not find sub/b.sql")
	}
}

func TestOSFile_ReadContent(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "content.sql")
	expected := "CREATE TABLE t (id INT);"
	os.WriteFile(filePath, []byte(expected), 0644)

	fs := NewOSFileSystem()
	d, err := fs.Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	var fileContent string
	d.Walk(func(f File, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if f.RelativePath() == "content.sql" {
			data, err := f.ReadContent()
			if err != nil {
				t.Fatalf("ReadContent() error = %v", err)
			}
			fileContent = string(data)
		}
		return nil
	})

	if fileContent != expected {
		t.Errorf("ReadContent() = %q, want %q", fileContent, expected)
	}
}

func TestParseMaxFileSize(t *testing.T) {
	for raw, want := range map[string]int64{"": DefaultMaxFileSize, "20971520": 20971520} {
		if got, err := parseMaxFileSize(raw); err != nil || got != want {
			t.Errorf("%q: got %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"10MB", "0", "-1", "1.5"} {
		_, err := parseMaxFileSize(raw)
		if !errors.Is(err, pgmi.ErrInvalidConfig) || !strings.Contains(err.Error(), "PGMI_MAX_FILE_SIZE") {
			t.Errorf("%q: want an invalid-config error naming the variable, got %v", raw, err)
		}
	}
}
