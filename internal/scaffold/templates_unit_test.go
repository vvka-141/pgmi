package scaffold

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/stretchr/testify/require"
)

// TestTemplateStructureWithoutFilesystem validates all embedded templates
// without requiring filesystem I/O. This tests the templates directly from
// the embedded FS, demonstrating the new EmbedFileSystem capability.
func TestTemplateStructureWithoutFilesystem(t *testing.T) {
	templates := []string{"basic", "advanced"}

	for _, templateName := range templates {
		t.Run(templateName, func(t *testing.T) {
			testTemplateStructure(t, templateName)
		})
	}
}

func testTemplateStructure(t *testing.T, templateName string) {
	t.Helper()

	// Create EmbedFileSystem from embedded templates
	templateRoot := "templates/" + templateName
	efs := filesystem.NewEmbedFileSystem(templatesFS, templateRoot)

	// Test 1: Verify deploy.sql exists and is readable
	t.Run("deploy.sql exists", func(t *testing.T) {
		deploySQLContent, err := efs.ReadFile("deploy.sql")
		require.NoError(t, err, "deploy.sql should exist in template")
		require.NotEmpty(t, deploySQLContent, "deploy.sql should not be empty")

		content := string(deploySQLContent)
		// Verify it contains essential SQL orchestration patterns
		require.Contains(t, content, "pg_temp.pgmi", "deploy.sql should reference pg_temp.pgmi functions")
		require.True(t,
			strings.Contains(content, "DO $$") || strings.Contains(content, "DO $") ||
				strings.Contains(content, "LANGUAGE plpgsql") || strings.Contains(content, "CREATE FUNCTION"),
			"deploy.sql should contain PL/pgSQL code")
	})

	// Test 2: Verify template has a README
	t.Run("README exists", func(t *testing.T) {
		readmeContent, err := efs.ReadFile("README.md")
		require.NoError(t, err, "README.md should exist in template")
		require.NotEmpty(t, readmeContent, "README.md should not be empty")
	})

	// Test 3: Scan directory structure without filesystem writes
	t.Run("directory scanning", func(t *testing.T) {
		calc := checksum.New()
		s := scanner.NewScannerWithFS(calc, efs)

		result, err := s.ScanDirectory(".")
		require.NoError(t, err, "Should be able to scan embedded template")
		require.NotEmpty(t, result.Files, "Template should contain SQL files")

		// Verify all files have valid checksums
		for _, file := range result.Files {
			require.NotEmpty(t, file.Checksum, "File %s should have normalized checksum", file.Path)
			require.NotEmpty(t, file.ChecksumRaw, "File %s should have raw checksum", file.Path)
			require.NotEmpty(t, file.Content, "File %s should have content", file.Path)
		}
	})

	// Test 4: Verify SQL files are valid (basic syntax check)
	t.Run("SQL files validity", func(t *testing.T) {
		dir, err := efs.Open(".")
		require.NoError(t, err)

		var sqlFileCount int
		err = dir.Walk(func(file filesystem.File, walkErr error) error {
			require.NoError(t, walkErr)

			if file.Info().IsDir() {
				return nil
			}

			// Check SQL files
			if strings.HasSuffix(file.Path(), ".sql") {
				sqlFileCount++

				content, err := file.ReadContent()
				require.NoError(t, err, "Should be able to read %s", file.Path())

				// Basic SQL validation - should not be empty
				contentStr := strings.TrimSpace(string(content))
				require.NotEmpty(t, contentStr, "SQL file %s should not be empty", file.Path())

				// Should contain some SQL keywords (very basic check)
				upperContent := strings.ToUpper(contentStr)
				hasSQLKeywords := strings.Contains(upperContent, "SELECT") ||
					strings.Contains(upperContent, "CREATE") ||
					strings.Contains(upperContent, "INSERT") ||
					strings.Contains(upperContent, "UPDATE") ||
					strings.Contains(upperContent, "DELETE") ||
					strings.Contains(upperContent, "DO $$") ||
					strings.Contains(upperContent, "BEGIN") ||
					strings.Contains(upperContent, "RAISE NOTICE")

				require.True(t, hasSQLKeywords,
					"SQL file %s should contain SQL keywords", file.Path())
			}

			return nil
		})

		require.NoError(t, err)
		require.Greater(t, sqlFileCount, 0, "Template should contain at least one SQL file")
	})

	// Test 5: Template-specific structure validation
	t.Run("template-specific structure", func(t *testing.T) {
		switch templateName {
		case "basic":
			// Basic template should have migrations directory
			info, err := efs.Stat("migrations")
			require.NoError(t, err, "Basic template should have migrations directory")
			require.True(t, info.IsDir(), "migrations should be a directory")

		case "advanced":
			// Advanced template should have api/ at root and lib/ with nested structure
			expectedDirs := []string{"api", "lib"}
			for _, dirName := range expectedDirs {
				info, err := efs.Stat(dirName)
				require.NoError(t, err, "Advanced template should have %s directory", dirName)
				require.True(t, info.IsDir(), "%s should be a directory", dirName)
			}
			// Verify lib/ contains the expected nested directories
			libDirs := []string{"lib/api", "lib/core", "lib/internal", "lib/utils"}
			for _, dirPath := range libDirs {
				info, err := efs.Stat(dirPath)
				require.NoError(t, err, "Advanced template should have %s directory", dirPath)
				require.True(t, info.IsDir(), "%s should be a directory", dirPath)
			}
		}
	})

	// Test 6: Verify no unexpected files (e.g., OS-specific files)
	t.Run("no unexpected files", func(t *testing.T) {
		dir, err := efs.Open(".")
		require.NoError(t, err)

		err = dir.Walk(func(file filesystem.File, walkErr error) error {
			require.NoError(t, walkErr)

			if file.Info().IsDir() {
				return nil
			}

			filename := filepath.Base(file.Path())

			// Check for OS-specific files that shouldn't be in templates
			require.NotEqual(t, ".DS_Store", filename, "Template should not contain .DS_Store")
			require.NotEqual(t, "Thumbs.db", filename, "Template should not contain Thumbs.db")
			require.NotContains(t, filename, "~", "Template should not contain backup files")

			return nil
		})

		require.NoError(t, err)
	})
}

// TestTemplateFileMetadata validates that file metadata is correctly extracted
// from embedded templates without filesystem I/O
func TestTemplateFileMetadata(t *testing.T) {
	templateName := "advanced"
	templateRoot := "templates/" + templateName
	efs := filesystem.NewEmbedFileSystem(templatesFS, templateRoot)

	calc := checksum.New()
	s := scanner.NewScannerWithFS(calc, efs)

	result, err := s.ScanDirectory(".")
	require.NoError(t, err)

	// Verify all files have correct metadata fields
	for _, file := range result.Files {
		t.Run(file.Path, func(t *testing.T) {
			// Path should use forward slashes
			require.NotContains(t, file.Path, "\\", "Path should use forward slashes")

			// Name should be just the filename
			require.Equal(t, filepath.Base(file.Path), file.Name)

			// Extension should match
			require.Equal(t, filepath.Ext(file.Path), file.Extension)

			// Directory should be the parent path or empty for root
			expectedDir := filepath.ToSlash(filepath.Dir(file.Path))
			if expectedDir == "." {
				expectedDir = ""
			}
			require.Equal(t, expectedDir, file.Directory)

			// Depth should be consistent with directory structure
			if file.Directory == "" {
				require.Equal(t, 0, file.Depth)
			} else {
				expectedDepth := strings.Count(file.Directory, "/") + 1
				require.Equal(t, expectedDepth, file.Depth)
			}

			// Size should match content length
			require.Equal(t, int64(len(file.Content)), file.SizeBytes)

			// ModifiedAt should be set (may be zero-time for embedded files)
			// For embedded files, ModTime() can be zero, which is acceptable
			_ = file.ModifiedAt // Just verify field exists
		})
	}
}

// TestTemplateDeploySQLReading validates deploy.sql can be read from embedded templates
func TestTemplateDeploySQLReading(t *testing.T) {
	templates := []string{"basic", "advanced"}

	for _, templateName := range templates {
		t.Run(templateName, func(t *testing.T) {
			templateRoot := "templates/" + templateName
			efs := filesystem.NewEmbedFileSystem(templatesFS, templateRoot)

			calc := checksum.New()
			s := scanner.NewScannerWithFS(calc, efs)

			// Test ValidateDeploySQL
			err := s.ValidateDeploySQL(".")
			require.NoError(t, err, "ValidateDeploySQL should pass for template %s", templateName)

			// Test ReadDeploySQL
			content, err := s.ReadDeploySQL(".")
			require.NoError(t, err, "ReadDeploySQL should succeed for template %s", templateName)
			require.NotEmpty(t, content, "deploy.sql content should not be empty")

			// Basic content validation
			require.Contains(t, content, "pg_temp.pgmi_source",
				"deploy.sql should reference pg_temp.pgmi_source table")
		})
	}
}

// TestTemplatePlaceholderExtraction validates placeholder extraction from embedded templates

// TestEmbedFileSystemPerformance validates that EmbedFileSystem performs well
func TestEmbedFileSystemPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	templateRoot := "templates/advanced"
	efs := filesystem.NewEmbedFileSystem(templatesFS, templateRoot)

	calc := checksum.New()
	s := scanner.NewScannerWithFS(calc, efs)

	// Run scan multiple times to verify performance
	iterations := 100
	for i := 0; i < iterations; i++ {
		result, err := s.ScanDirectory(".")
		require.NoError(t, err)
		require.NotEmpty(t, result.Files)
	}

	// This test primarily ensures no memory leaks or performance degradation
	// The exact timing is not critical, but it should complete reasonably fast
}
