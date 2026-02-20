package filesystem

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// osFile implements File interface for OS filesystem
type osFile struct {
	absPath string
	relPath string
	info    fs.FileInfo
}

func (f *osFile) Path() string         { return f.absPath }
func (f *osFile) RelativePath() string { return f.relPath }
func (f *osFile) Info() FileInfo       { return f.info }

func (f *osFile) ReadContent() ([]byte, error) {
	return os.ReadFile(f.absPath)
}

// osDirectory implements Directory interface for OS filesystem
type osDirectory struct {
	absPath string
}

func (d *osDirectory) Path() string { return d.absPath }

func (d *osDirectory) Walk(fn func(File, error) error) error {
	return filepath.WalkDir(d.absPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fn(nil, walkErr)
		}

		relPath, relErr := filepath.Rel(d.absPath, path)
		if relErr != nil {
			return fn(nil, fmt.Errorf("failed to get relative path: %w", relErr))
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return fn(nil, fmt.Errorf("failed to get file info: %w", infoErr))
		}

		file := &osFile{
			absPath: path,
			relPath: relPath,
			info:    info,
		}

		return fn(file, nil)
	})
}

// OSFileSystem implements FileSystemProvider for the OS filesystem
type OSFileSystem struct{}

// NewOSFileSystem creates a new OS filesystem provider
func NewOSFileSystem() *OSFileSystem {
	return &OSFileSystem{}
}


func (p *OSFileSystem) Open(path string) (Directory, error) {
	// Verify path exists and is a directory
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to access path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", path)
	}

	// Get absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	return &osDirectory{absPath: absPath}, nil
}

func (p *OSFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (p *OSFileSystem) Stat(path string) (FileInfo, error) {
	// os.Stat returns os.FileInfo which implements fs.FileInfo
	return os.Stat(path)
}
