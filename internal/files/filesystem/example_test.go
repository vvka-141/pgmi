package filesystem_test

import (
	"embed"
	"fmt"
	"log"

	"github.com/vvka-141/pgmi/internal/files/filesystem"
)

//go:embed testdata
var exampleFS embed.FS

// Example_embedFileSystem demonstrates using EmbedFileSystem to read files from embedded resources
func Example_embedFileSystem() {
	// Create an EmbedFileSystem wrapping embedded resources
	efs := filesystem.NewEmbedFileSystem(exampleFS, "testdata")

	// Read a file directly
	content, err := efs.ReadFile("root.sql")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Content: %s", string(content))

	// Output:
	// Content: SELECT 1;
}

// Example_embedFileSystem_walk demonstrates walking a directory tree from embedded resources
func Example_embedFileSystem_walk() {
	// Create an EmbedFileSystem wrapping embedded resources
	efs := filesystem.NewEmbedFileSystem(exampleFS, "testdata")

	// Open the root directory
	dir, err := efs.Open(".")
	if err != nil {
		log.Fatal(err)
	}

	// Walk the directory tree
	var fileCount int
	err = dir.Walk(func(file filesystem.File, err error) error {
		if err != nil {
			return err
		}
		if !file.Info().IsDir() {
			fileCount++
			fmt.Printf("Found file: %s\n", file.RelativePath())
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Total files: %d\n", fileCount)

	// Output:
	// Found file: root.sql
	// Found file: subdir/nested.sql
	// Total files: 2
}

// Example_memoryFileSystem demonstrates using MemoryFileSystem for testing
func Example_memoryFileSystem() {
	// Create an in-memory filesystem
	mfs := filesystem.NewMemoryFileSystem("/test")

	// Add files
	mfs.AddFile("migrations/001_init.sql", "CREATE TABLE users (id INT);")
	mfs.AddFile("migrations/002_data.sql", "INSERT INTO users VALUES (1);")

	// Read a file
	content, err := mfs.ReadFile("migrations/001_init.sql")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Migration content: %s\n", string(content))

	// Open and walk the directory
	dir, err := mfs.Open("/test/migrations")
	if err != nil {
		log.Fatal(err)
	}

	var fileCount int
	err = dir.Walk(func(file filesystem.File, err error) error {
		if err != nil {
			return err
		}
		if !file.Info().IsDir() {
			fileCount++
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Total migration files: %d\n", fileCount)

	// Output:
	// Migration content: CREATE TABLE users (id INT);
	// Total migration files: 2
}

// Example_fileSystemProvider demonstrates the FileSystemProvider abstraction
func Example_fileSystemProvider() {
	// Function that works with any FileSystemProvider implementation
	countFiles := func(fsProvider filesystem.FileSystemProvider, path string) (int, error) {
		dir, err := fsProvider.Open(path)
		if err != nil {
			return 0, err
		}

		count := 0
		err = dir.Walk(func(file filesystem.File, err error) error {
			if err != nil {
				return err
			}
			if !file.Info().IsDir() {
				count++
			}
			return nil
		})
		return count, err
	}

	// Use with EmbedFileSystem
	efs := filesystem.NewEmbedFileSystem(exampleFS, "testdata")
	embedCount, err := countFiles(efs, ".")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Embedded files: %d\n", embedCount)

	// Use with MemoryFileSystem
	mfs := filesystem.NewMemoryFileSystem("/test")
	mfs.AddFile("file1.sql", "SELECT 1;")
	mfs.AddFile("file2.sql", "SELECT 2;")
	memCount, err := countFiles(mfs, "/test")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Memory files: %d\n", memCount)

	// Output:
	// Embedded files: 2
	// Memory files: 2
}

// Example_embedFileSystem_pathNormalization demonstrates cross-platform path handling
func Example_embedFileSystem_pathNormalization() {
	efs := filesystem.NewEmbedFileSystem(exampleFS, "testdata")

	// All these path formats work correctly
	paths := []string{
		"subdir/nested.sql",    // Unix-style (forward slashes)
		"subdir\\nested.sql",   // Windows-style (backslashes)
		"./subdir/nested.sql",  // Relative with ./ prefix
	}

	for _, p := range paths {
		content, err := efs.ReadFile(p)
		if err != nil {
			log.Fatal(err)
		}
		// All paths resolve to the same file
		_ = content
	}

	fmt.Println("All path formats resolved successfully")

	// Output:
	// All path formats resolved successfully
}

// Example_memoryFileSystem_testFixture demonstrates using MemoryFileSystem for test fixtures
func Example_memoryFileSystem_testFixture() {
	// Create a test fixture with predefined files
	createTestFixture := func() filesystem.FileSystemProvider {
		mfs := filesystem.NewMemoryFileSystem("/project")
		mfs.AddFile("deploy.sql", "-- Deploy script")
		mfs.AddFile("migrations/001_init.sql", "CREATE SCHEMA app;")
		mfs.AddFile("migrations/002_users.sql", "CREATE TABLE app.users (id INT);")
		mfs.AddFile("setup/functions.sql", "CREATE FUNCTION app.hello() RETURNS TEXT AS $$ SELECT 'Hello'; $$ LANGUAGE SQL;")
		return mfs
	}

	// Use in tests
	fs := createTestFixture()

	// Verify deploy.sql exists
	if _, err := fs.Stat("deploy.sql"); err != nil {
		log.Fatal("deploy.sql not found")
	}
	fmt.Println("Deploy script: exists")

	// Count migration files
	dir, _ := fs.Open("/project/migrations")
	migrationCount := 0
	dir.Walk(func(file filesystem.File, err error) error {
		if !file.Info().IsDir() {
			migrationCount++
		}
		return nil
	})
	fmt.Printf("Migration files: %d\n", migrationCount)

	// Output:
	// Deploy script: exists
	// Migration files: 2
}
