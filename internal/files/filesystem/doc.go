// Package filesystem provides filesystem abstraction interfaces and implementations.
//
// This package defines interfaces for file and directory operations, enabling
// testability through in-memory implementations while maintaining compatibility
// with the OS filesystem.
//
// Key interfaces:
//   - FileSystemProvider: Factory for creating directory instances
//   - Directory: Represents a directory that can be traversed
//   - File: Represents an individual file with metadata and content
//   - FileInfo: File metadata similar to os.FileInfo
//
// Implementations:
//   - OSFileSystemProvider: Production implementation using OS filesystem
//   - MemoryFileSystem: In-memory implementation for testing
package filesystem
