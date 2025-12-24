package filesystem

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryFileSystem_Basic(t *testing.T) {
	mfs := NewMemoryFileSystem("/test/project")

	// Add some files
	mfs.AddFile("root.sql", "SELECT 1;")
	mfs.AddFile("migrations/001_users.sql", "CREATE TABLE users (id INT);")

	// Try to open the root directory
	dir, err := mfs.Open("/test/project")
	require.NoError(t, err, "Failed to open root directory")
	require.NotNil(t, dir)

	// Verify we can walk the directory
	var fileCount int
	err = dir.Walk(func(file File, err error) error {
		require.NoError(t, err)
		if !file.Info().IsDir() {
			fileCount++
			t.Logf("Found file: %s (rel: %s)", file.Path(), file.RelativePath())
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, fileCount, "Expected 2 files")
}

func TestMemoryFileSystem_ReadFile(t *testing.T) {
	mfs := NewMemoryFileSystem("/test/project")

	// Add a file
	expectedContent := "SELECT 1;"
	mfs.AddFile("root.sql", expectedContent)

	// Read it back
	content, err := mfs.ReadFile("/test/project/root.sql")
	require.NoError(t, err)
	require.Equal(t, expectedContent, string(content))
}

func TestMemoryFileSystem_Stat(t *testing.T) {
	mfs := NewMemoryFileSystem("/test/project")

	// Add a file
	mfs.AddFile("root.sql", "SELECT 1;")

	// Stat the file
	info, err := mfs.Stat("/test/project/root.sql")
	require.NoError(t, err)
	require.False(t, info.IsDir())
	require.Equal(t, "root.sql", info.Name())

	// Stat the root directory
	info, err = mfs.Stat("/test/project")
	require.NoError(t, err)
	require.True(t, info.IsDir())
}
