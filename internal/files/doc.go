// Package files provides file-related functionality organized into sub-packages.
//
// This package has been refactored into the following sub-packages:
//   - filesystem: Filesystem abstraction interfaces and implementations (OS and in-memory)
//   - scanner: File discovery and metadata extraction
//   - loader: Database loading operations for session-scoped tables
//
// # Usage
//
//	import (
//	    "github.com/vvka-141/pgmi/internal/files/filesystem"
//	    "github.com/vvka-141/pgmi/internal/files/scanner"
//	    "github.com/vvka-141/pgmi/internal/files/loader"
//	)
//
//	// Create scanner
//	fileScanner := scanner.NewScanner(checksum.New())
//	result, err := fileScanner.ScanDirectory("./migrations")
//
//	// Load files into database
//	fileLoader := loader.NewLoader()
//	err = fileLoader.LoadFilesIntoSession(ctx, pool, result.Files)
//
// # Organization
//
// This organization follows the Single Responsibility Principle, with each
// sub-package focused on a specific concern:
//   - filesystem: Provides filesystem abstraction for testability
//   - scanner: Handles file discovery, checksum calculation, and placeholder detection
//   - loader: Manages database operations for loading files and parameters
package files
