package filesystem

import (
	"io/fs"
)

// FileInfo is an alias for fs.FileInfo from the standard library.
// This provides compatibility with the fs.FS ecosystem while maintaining
// a stable local type for our abstraction layer.
type FileInfo = fs.FileInfo

// File represents an individual file with its metadata and content accessor
type File interface {
	// Path returns the absolute path to the file
	Path() string

	// RelativePath returns the path relative to the source root
	RelativePath() string

	// Info returns file metadata
	Info() FileInfo

	// ReadContent returns the file's content
	ReadContent() ([]byte, error)
}

// Directory represents a directory that can be traversed to discover files
type Directory interface {
	// Path returns the absolute path to the directory
	Path() string

	// Walk traverses the directory tree, calling the provided function for each file and directory
	// The function receives the file/directory and any error encountered
	// If the function returns an error, walking stops
	Walk(fn func(File, error) error) error
}

// FileSystemProvider is a factory for creating Directory instances
type FileSystemProvider interface {
	// Open opens a directory at the specified path
	Open(path string) (Directory, error)

	// ReadFile reads a specific file at the given path
	ReadFile(path string) ([]byte, error)

	// ReadDir reads the directory entries at the given path.
	// This is a convenience method that returns a flat list of entries
	// without requiring Walk() for simple directory listing.
	ReadDir(path string) ([]FileInfo, error)

	// Stat returns file information for the given path
	Stat(path string) (FileInfo, error)
}
