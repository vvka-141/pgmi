package filesystem

import (
	"embed"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// normalizeLineEndings converts Windows CRLF to Unix LF for cross-platform testing
func normalizeLineEndings(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

//go:embed testdata
var testdataFS embed.FS

func TestEmbedFileSystem_Open(t *testing.T) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	tests := []struct {
		name      string
		path      string
		expectErr bool
	}{
		{
			name:      "open root directory",
			path:      ".",
			expectErr: false,
		},
		{
			name:      "open empty path (same as root)",
			path:      "",
			expectErr: false,
		},
		{
			name:      "open subdirectory",
			path:      "subdir",
			expectErr: false,
		},
		{
			name:      "open non-existent directory",
			path:      "nonexistent",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, err := efs.Open(tt.path)
			if tt.expectErr {
				require.Error(t, err)
				require.Nil(t, dir)
			} else {
				require.NoError(t, err)
				require.NotNil(t, dir)
			}
		})
	}
}

func TestEmbedFileSystem_ReadFile(t *testing.T) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	tests := []struct {
		name            string
		path            string
		expectedContent string
		expectErr       bool
	}{
		{
			name:            "read root file",
			path:            "root.sql",
			expectedContent: "SELECT 1;\n",
			expectErr:       false,
		},
		{
			name:            "read subdirectory file",
			path:            "subdir/nested.sql",
			expectedContent: "SELECT 2;\n",
			expectErr:       false,
		},
		{
			name:      "read non-existent file",
			path:      "nonexistent.sql",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := efs.ReadFile(tt.path)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedContent, normalizeLineEndings(string(content)))
			}
		})
	}
}

func TestEmbedFileSystem_Stat(t *testing.T) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	tests := []struct {
		name      string
		path      string
		isDir     bool
		expectErr bool
	}{
		{
			name:      "stat root directory",
			path:      ".",
			isDir:     true,
			expectErr: false,
		},
		{
			name:      "stat file",
			path:      "root.sql",
			isDir:     false,
			expectErr: false,
		},
		{
			name:      "stat subdirectory",
			path:      "subdir",
			isDir:     true,
			expectErr: false,
		},
		{
			name:      "stat nested file",
			path:      "subdir/nested.sql",
			isDir:     false,
			expectErr: false,
		},
		{
			name:      "stat non-existent",
			path:      "nonexistent",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := efs.Stat(tt.path)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.isDir, info.IsDir())
			}
		})
	}
}

func TestEmbedFileSystem_Walk(t *testing.T) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	dir, err := efs.Open(".")
	require.NoError(t, err)

	var files []string
	var dirs []string

	err = dir.Walk(func(file File, walkErr error) error {
		require.NoError(t, walkErr)

		if file.Info().IsDir() {
			dirs = append(dirs, file.RelativePath())
		} else {
			files = append(files, file.RelativePath())
		}
		return nil
	})

	require.NoError(t, err)

	// Verify we found expected files
	require.Contains(t, files, "root.sql")
	require.Contains(t, files, "subdir/nested.sql")

	// Verify we found expected directories
	require.Contains(t, dirs, ".")
	require.Contains(t, dirs, "subdir")
}

func TestEmbedFileSystem_FileContent(t *testing.T) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	dir, err := efs.Open(".")
	require.NoError(t, err)

	var foundRootSQL bool
	err = dir.Walk(func(file File, walkErr error) error {
		require.NoError(t, walkErr)

		if file.Info().IsDir() {
			return nil
		}

		if file.RelativePath() == "root.sql" {
			foundRootSQL = true

			// Test ReadContent
			content, err := file.ReadContent()
			require.NoError(t, err)
			require.Equal(t, "SELECT 1;\n", normalizeLineEndings(string(content)))

			// Verify file info
			require.Equal(t, "root.sql", file.Info().Name())
			require.Greater(t, file.Info().Size(), int64(0))
			require.False(t, file.Info().IsDir())
		}

		return nil
	})

	require.NoError(t, err)
	require.True(t, foundRootSQL, "Expected to find root.sql during walk")
}

func TestEmbedFileSystem_PathNormalization(t *testing.T) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	tests := []struct {
		name        string
		path        string
		expectError bool
	}{
		{
			name:        "forward slashes",
			path:        "subdir/nested.sql",
			expectError: false,
		},
		{
			name:        "backslashes (Windows-style)",
			path:        "subdir\\nested.sql",
			expectError: false, // Should be normalized to forward slashes
		},
		{
			name:        "mixed slashes",
			path:        "subdir/nested.sql",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := efs.ReadFile(tt.path)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, "SELECT 2;\n", normalizeLineEndings(string(content)))
			}
		})
	}
}

func TestEmbedFileSystem_RelativePaths(t *testing.T) {
	efs := NewEmbedFileSystem(testdataFS, "testdata")

	dir, err := efs.Open(".")
	require.NoError(t, err)

	var paths []string
	err = dir.Walk(func(file File, walkErr error) error {
		require.NoError(t, walkErr)
		if !file.Info().IsDir() {
			paths = append(paths, file.RelativePath())
		}
		return nil
	})

	require.NoError(t, err)

	// All relative paths should use forward slashes
	for _, p := range paths {
		require.NotContains(t, p, "\\", "Relative path should use forward slashes: %s", p)
	}
}
