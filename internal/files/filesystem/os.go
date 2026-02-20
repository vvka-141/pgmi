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
	return filepath.Walk(d.absPath, func(path string, info os.FileInfo, walkErr error) error {
		var callbackErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					callbackErr = fmt.Errorf("walk callback panicked at %s: %v", path, r)
				}
			}()

			if walkErr != nil {
				callbackErr = fn(nil, walkErr)
				return
			}

			relPath, relErr := filepath.Rel(d.absPath, path)
			if relErr != nil {
				callbackErr = fn(nil, fmt.Errorf("failed to get relative path: %w", relErr))
				return
			}

			file := &osFile{
				absPath: path,
				relPath: relPath,
				info:    info,
			}

			callbackErr = fn(file, nil)
		}()

		return callbackErr
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

func (p *OSFileSystem) ReadDir(path string) ([]FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	result := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("failed to get file info for %s: %w", entry.Name(), err)
		}
		result = append(result, info)
	}

	return result, nil
}

func (p *OSFileSystem) Stat(path string) (FileInfo, error) {
	// os.Stat returns os.FileInfo which implements fs.FileInfo
	return os.Stat(path)
}
