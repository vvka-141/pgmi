package filesystem

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// embedFile implements File interface for embed.FS
type embedFile struct {
	embedFS *embed.FS
	absPath string // path within embed.FS (always uses forward slashes)
	relPath string // relative path from root
	info    fs.FileInfo
}

func (f *embedFile) Path() string         { return f.absPath }
func (f *embedFile) RelativePath() string { return f.relPath }
func (f *embedFile) Info() FileInfo       { return f.info }

func (f *embedFile) ReadContent() ([]byte, error) {
	return f.embedFS.ReadFile(f.absPath)
}

// embedDirectory implements Directory interface for embed.FS
type embedDirectory struct {
	embedFS *embed.FS
	absPath string // path within embed.FS (always uses forward slashes)
	root    string // root path for calculating relative paths
}

func (d *embedDirectory) Path() string { return d.absPath }

func (d *embedDirectory) Walk(fn func(File, error) error) error {
	return fs.WalkDir(d.embedFS, d.absPath, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fn(nil, err)
		}

		info, err := entry.Info()
		if err != nil {
			return fn(nil, fmt.Errorf("failed to get file info for %s: %w", filePath, err))
		}

		relPath, err := filepath.Rel(d.root, filePath)
		if err != nil {
			return fn(nil, fmt.Errorf("failed to calculate relative path: %w", err))
		}
		relPath = strings.ReplaceAll(relPath, "\\", "/")

		file := &embedFile{
			embedFS: d.embedFS,
			absPath: filePath,
			relPath: relPath,
			info:    info,
		}

		return fn(file, nil)
	})
}

// EmbedFileSystem implements FileSystemProvider for embed.FS
type EmbedFileSystem struct {
	embedFS embed.FS
	root    string // root path within the embed.FS (always uses forward slashes)
}

// NewEmbedFileSystem creates a new filesystem provider wrapping an embed.FS.
// The root parameter specifies the subdirectory within the embed.FS to treat as the root.
// All paths are normalized to use forward slashes for consistency with embed.FS.
func NewEmbedFileSystem(embedFS embed.FS, root string) *EmbedFileSystem {
	// Normalize root path to use forward slashes and remove trailing slash
	root = path.Clean(root)
	return &EmbedFileSystem{
		embedFS: embedFS,
		root:    root,
	}
}

// Open implements FileSystemProvider.Open
func (efs *EmbedFileSystem) Open(openPath string) (Directory, error) {
	// Normalize path to forward slashes
	openPath = strings.ReplaceAll(openPath, "\\", "/")

	// Calculate absolute path within embed.FS
	var absPath string
	if openPath == "." || openPath == "" {
		absPath = efs.root
	} else if strings.HasPrefix(openPath, "/") || path.IsAbs(openPath) {
		// If path is absolute, use it as-is (already relative to embed.FS root)
		absPath = openPath
	} else {
		// Relative path - join with root
		absPath = path.Join(efs.root, openPath)
	}

	// Clean the path
	absPath = path.Clean(absPath)

	// Verify path exists and is a directory
	entries, err := efs.embedFS.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open directory %s: %w", openPath, err)
	}

	// ReadDir only works on directories, so if we got here, it's a directory
	_ = entries // ReadDir succeeds only for directories

	return &embedDirectory{
		embedFS: &efs.embedFS,
		absPath: absPath,
		root:    efs.root,
	}, nil
}

// ReadFile implements FileSystemProvider.ReadFile
func (efs *EmbedFileSystem) ReadFile(filePath string) ([]byte, error) {
	// Normalize path to forward slashes (explicit replace for cross-platform compatibility)
	filePath = strings.ReplaceAll(filePath, "\\", "/")

	// Calculate absolute path within embed.FS
	var absPath string
	if strings.HasPrefix(filePath, "/") || path.IsAbs(filePath) {
		absPath = filePath
	} else {
		absPath = path.Join(efs.root, filePath)
	}

	// Clean the path
	absPath = path.Clean(absPath)

	content, err := efs.embedFS.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	return content, nil
}

// Stat implements FileSystemProvider.Stat
func (efs *EmbedFileSystem) Stat(statPath string) (FileInfo, error) {
	// Normalize path to forward slashes
	statPath = strings.ReplaceAll(statPath, "\\", "/")

	// Calculate absolute path within embed.FS
	var absPath string
	if strings.HasPrefix(statPath, "/") || path.IsAbs(statPath) {
		absPath = statPath
	} else {
		absPath = path.Join(efs.root, statPath)
	}

	// Clean the path
	absPath = path.Clean(absPath)

	// fs.Stat returns fs.FileInfo directly
	info, err := fs.Stat(efs.embedFS, absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat path %s: %w", statPath, err)
	}

	return info, nil
}
