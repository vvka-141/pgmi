package filesystem

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const DefaultMaxFileSize int64 = 10 * 1024 * 1024 // 10 MiB

var maxFileSize = sync.OnceValue(func() int64 {
	if raw := os.Getenv("PGMI_MAX_FILE_SIZE"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return DefaultMaxFileSize
})

// osFile implements File interface for OS filesystem
type osFile struct {
	absPath string
	relPath string
	info    fs.FileInfo
}

func (f *osFile) Path() string         { return f.absPath }
func (f *osFile) RelativePath() string { return f.relPath }
func (f *osFile) Info() FileInfo       { return f.info }

// ReadContent reads the file with two hardening steps beyond os.ReadFile:
//   - Reject symlinks via os.Lstat before opening, so a malicious symlink
//     named *.sql that points at /etc/shadow is not followed.
//   - Cap read size at PGMI_MAX_FILE_SIZE (default 10 MiB) via io.LimitReader,
//     so a multi-GB binary that landed in the project tree cannot OOM the
//     deploy or ship an unbounded INSERT to PostgreSQL.
func (f *osFile) ReadContent() ([]byte, error) {
	linkInfo, err := os.Lstat(f.absPath)
	if err != nil {
		return nil, fmt.Errorf("lstat %s: %w", f.relPath, err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to read symlink %s (symlinks are rejected by the scanner to avoid path-escape)", f.relPath)
	}

	cap := maxFileSize()
	if linkInfo.Size() > cap {
		return nil, fmt.Errorf("file %s is %d bytes, exceeds %d-byte cap (override via PGMI_MAX_FILE_SIZE)", f.relPath, linkInfo.Size(), cap)
	}

	file, err := os.Open(f.absPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", f.relPath, err)
	}
	defer file.Close()

	// +1 so ReadAll reads one extra byte if the file grew between Lstat and Open;
	// we then detect that and return an explicit error instead of silently truncating.
	data, err := io.ReadAll(io.LimitReader(file, cap+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.relPath, err)
	}
	if int64(len(data)) > cap {
		return nil, fmt.Errorf("file %s grew past the %d-byte cap during read (override via PGMI_MAX_FILE_SIZE)", f.relPath, cap)
	}
	return data, nil
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
