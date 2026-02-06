package testdiscovery

import (
	"strings"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// ConvertFromFileMetadata converts FileMetadata slice to Source slice for test discovery.
func ConvertFromFileMetadata(files []pgmi.FileMetadata) []Source {
	sources := make([]Source, 0, len(files))
	for _, file := range files {
		sources = append(sources, Source{
			Path:       file.Path,
			Directory:  ensureTrailingSlash(file.Directory),
			Filename:   file.Name,
			Content:    file.Content,
			IsSQLFile:  isSQLExtension(file.Extension),
			IsTestFile: pgmi.IsTestPath(file.Path),
		})
	}
	return sources
}

// ensureTrailingSlash ensures the directory has a trailing slash.
// Handles the empty root directory case.
func ensureTrailingSlash(dir string) string {
	if dir == "" || dir == "." {
		return "./"
	}
	if !strings.HasSuffix(dir, "/") {
		return dir + "/"
	}
	return dir
}

// isSQLExtension checks if the extension indicates a SQL file.
func isSQLExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".sql", ".ddl", ".dml", ".dql", ".dcl", ".psql", ".pgsql", ".plpgsql":
		return true
	default:
		return false
	}
}
