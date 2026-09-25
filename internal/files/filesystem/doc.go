// Package filesystem provides filesystem abstraction interfaces and implementations.
//
// This package defines interfaces for file and directory operations. Test
// doubles live in internal/files/fakefs so they stay out of the binary.
//
// Key interfaces:
//   - FileSystemProvider: Factory for creating directory instances
//   - Directory: Represents a directory that can be traversed
//   - File: Represents an individual file with metadata and content
//   - FileInfo: File metadata similar to os.FileInfo
//
// OSFileSystem is the only implementation the binary uses.
package filesystem
