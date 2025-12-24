// Package scanner provides file discovery and metadata extraction for SQL files.
//
// The scanner package is responsible for:
//   - Recursively discovering SQL files in a directory tree
//   - Extracting file metadata (path, size, timestamps, checksums)
//   - Detecting placeholder variables in file content
//   - Validating the presence of deploy.sql orchestrator script
//
// The scanner is designed to be filesystem-agnostic through the use of
// filesystem.FileSystemProvider interface, enabling both production use
// with the OS filesystem and testing with in-memory filesystems.
package scanner
