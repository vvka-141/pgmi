package filesystem

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// memoryFileInfo implements fs.FileInfo for in-memory files
type memoryFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (f *memoryFileInfo) Name() string       { return f.name }
func (f *memoryFileInfo) Size() int64        { return f.size }
func (f *memoryFileInfo) Mode() fs.FileMode  { return f.mode }
func (f *memoryFileInfo) ModTime() time.Time { return f.modTime }
func (f *memoryFileInfo) IsDir() bool        { return f.isDir }
func (f *memoryFileInfo) Sys() interface{}   { return nil }

// memoryFile implements File interface for in-memory files
type memoryFile struct {
	absPath string
	relPath string
	content []byte
	info    fs.FileInfo
}

func (f *memoryFile) Path() string         { return f.absPath }
func (f *memoryFile) RelativePath() string { return f.relPath }
func (f *memoryFile) Info() FileInfo       { return f.info }

func (f *memoryFile) ReadContent() ([]byte, error) {
	return f.content, nil
}

// memoryDirectory implements Directory interface for in-memory filesystem
type memoryDirectory struct {
	absPath string
	fs      *MemoryFileSystem
}

func (d *memoryDirectory) Path() string { return d.absPath }

func (d *memoryDirectory) Walk(fn func(File, error) error) error {
	// Get all files and directories under this path
	entries := d.fs.getEntriesUnder(d.absPath)

	// Sort by path for deterministic order
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].absPath < entries[j].absPath
	})

	for _, entry := range entries {
		// Recover from panics in callback to prevent crashing the entire walk
		var callbackErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					// Convert panic to error
					callbackErr = fmt.Errorf("walk callback panicked at %s: %v", entry.absPath, r)
				}
			}()

			callbackErr = fn(entry, nil)
		}()

		// If callback returned an error (or panicked), stop walking
		if callbackErr != nil {
			return callbackErr
		}
	}

	return nil
}

// MemoryFileSystem implements FileSystemProvider for in-memory testing
type MemoryFileSystem struct {
	files map[string]*memoryFile // map of absolute path -> file
	root  string                  // root directory path
}

// NewMemoryFileSystem creates a new in-memory filesystem.
// The root path is normalized to use forward slashes for virtual filesystem consistency.
func NewMemoryFileSystem(root string) *MemoryFileSystem {
	// Normalize root to forward slashes (virtual filesystem convention)
	root = filepath.ToSlash(root)
	root = path.Clean(root)

	mfs := &MemoryFileSystem{
		files: make(map[string]*memoryFile),
		root:  root,
	}

	// Create the root directory entry
	mfs.files[root] = &memoryFile{
		absPath: root,
		relPath: ".",
		content: nil,
		info: &memoryFileInfo{
			name:    path.Base(root),
			size:    0,
			mode:    0755 | fs.ModeDir,
			modTime: time.Now(),
			isDir:   true,
		},
	}

	return mfs
}

// AddFile adds a file to the in-memory filesystem
func (mfs *MemoryFileSystem) AddFile(path string, content string) {
	mfs.AddFileWithTime(path, content, time.Now())
}

// AddFileWithTime adds a file with a specific modification time
func (mfs *MemoryFileSystem) AddFileWithTime(filePath string, content string, modTime time.Time) {
	// Normalize path to forward slashes (virtual filesystem convention)
	filePath = filepath.ToSlash(filePath)

	// Calculate absolute path within virtual filesystem
	var absPath string
	if strings.HasPrefix(filePath, "/") || path.IsAbs(filePath) {
		absPath = filePath
	} else {
		absPath = path.Join(mfs.root, filePath)
	}
	absPath = path.Clean(absPath)

	// Calculate relative path from root
	relPath, err := filepath.Rel(mfs.root, absPath)
	if err != nil {
		relPath = filePath
	}
	relPath = filepath.ToSlash(relPath)

	contentBytes := []byte(content)

	file := &memoryFile{
		absPath: absPath,
		relPath: relPath,
		content: contentBytes,
		info: &memoryFileInfo{
			name:    path.Base(absPath),
			size:    int64(len(contentBytes)),
			mode:    0644,
			modTime: modTime,
			isDir:   false,
		},
	}

	mfs.files[absPath] = file

	// Also add parent directories
	mfs.ensureDirectoriesExist(absPath)
}

// ensureDirectoriesExist creates directory entries for all parent directories
func (mfs *MemoryFileSystem) ensureDirectoriesExist(filePath string) {
	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == mfs.root {
		return
	}

	// Check if directory entry already exists
	if _, exists := mfs.files[dir]; exists {
		return
	}

	// Create directory entry
	mfs.files[dir] = &memoryFile{
		absPath: dir,
		relPath: strings.TrimPrefix(dir, mfs.root+"/"),
		content: nil,
		info: &memoryFileInfo{
			name:    path.Base(dir),
			size:    0,
			mode:    0755 | fs.ModeDir,
			modTime: time.Now(),
			isDir:   true,
		},
	}

	// Recursively create parent directories
	mfs.ensureDirectoriesExist(dir)
}

// getEntriesUnder returns all files and directories under the given path
func (mfs *MemoryFileSystem) getEntriesUnder(basePath string) []*memoryFile {
	basePath = filepath.ToSlash(basePath)
	var entries []*memoryFile

	for path, file := range mfs.files {
		// Special handling for root directory to avoid double slashes
		var matched bool
		if basePath == "/" {
			// For root, include all paths starting with "/"
			matched = strings.HasPrefix(path, "/")
		} else {
			// For subdirectories, check exact match or prefix with trailing slash
			matched = path == basePath || strings.HasPrefix(path, basePath+"/")
		}

		if matched {
			entries = append(entries, file)
		}
	}

	return entries
}

// Open implements FileSystemProvider.Open
func (mfs *MemoryFileSystem) Open(openPath string) (Directory, error) {
	// Normalize to forward slashes (virtual filesystem convention)
	openPath = filepath.ToSlash(openPath)

	// Calculate absolute path within virtual filesystem
	var absPath string
	if openPath == "." || openPath == "" {
		absPath = mfs.root
	} else if strings.HasPrefix(openPath, "/") || path.IsAbs(openPath) {
		absPath = openPath
	} else {
		absPath = path.Join(mfs.root, openPath)
	}
	absPath = path.Clean(absPath)

	// Check if path exists as a directory
	file, exists := mfs.files[absPath]
	if exists {
		if !file.info.IsDir() {
			return nil, fmt.Errorf("path is not a directory: %s", openPath)
		}
		// Directory exists, return it
		return &memoryDirectory{
			absPath: absPath,
			fs:      mfs,
		}, nil
	}

	// Even if directory doesn't have an explicit entry, allow it if there are files under it
	hasEntries := false
	for filePath := range mfs.files {
		if strings.HasPrefix(filePath, absPath+"/") || filePath == absPath {
			hasEntries = true
			break
		}
	}

	if !hasEntries {
		return nil, fmt.Errorf("directory not found: %s", openPath)
	}

	return &memoryDirectory{
		absPath: absPath,
		fs:      mfs,
	}, nil
}

// ReadFile implements FileSystemProvider.ReadFile
func (mfs *MemoryFileSystem) ReadFile(filePath string) ([]byte, error) {
	// Normalize to forward slashes (virtual filesystem convention)
	filePath = filepath.ToSlash(filePath)

	// Calculate absolute path within virtual filesystem
	var absPath string
	if strings.HasPrefix(filePath, "/") || path.IsAbs(filePath) {
		absPath = filePath
	} else {
		absPath = path.Join(mfs.root, filePath)
	}
	absPath = path.Clean(absPath)

	file, exists := mfs.files[absPath]
	if !exists {
		return nil, fmt.Errorf("file not found: %s", filePath)
	}

	if file.info.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file: %s", filePath)
	}

	return file.content, nil
}

// Stat implements FileSystemProvider.Stat
func (mfs *MemoryFileSystem) Stat(statPath string) (FileInfo, error) {
	// Normalize to forward slashes (virtual filesystem convention)
	statPath = filepath.ToSlash(statPath)

	// Calculate absolute path within virtual filesystem
	var absPath string
	if strings.HasPrefix(statPath, "/") || path.IsAbs(statPath) {
		absPath = statPath
	} else {
		absPath = path.Join(mfs.root, statPath)
	}
	absPath = path.Clean(absPath)

	file, exists := mfs.files[absPath]
	if !exists {
		return nil, fmt.Errorf("path not found: %s", statPath)
	}

	return file.info, nil
}
