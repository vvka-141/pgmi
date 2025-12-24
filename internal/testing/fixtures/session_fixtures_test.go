package fixtures

import (
	"testing"

	"github.com/vvka-141/pgmi/internal/files/filesystem"
)

// TestSessionFixtureBuilder_StandardMultiLevel validates the StandardMultiLevel fixture
// generates the expected file structure.
func TestSessionFixtureBuilder_StandardMultiLevel(t *testing.T) {
	fs := StandardMultiLevel()

	// Expected files and their count
	expectedFiles := map[string]bool{
		// Regular files
		"deploy.sql":              true,
		"migrations/001_schema.sql": true,
		"migrations/002_data.sql":   true,
		"setup/functions.sql":       true,

		// Test files (should be under __test__/)
		"__test__/_setup.sql":                true,
		"__test__/test_basic.sql":            true,
		"__test__/auth/_setup.sql":           true,
		"__test__/auth/test_login.sql":       true,
		"__test__/auth/oauth/test_google.sql": true,
		"__test__/billing/test_stripe.sql":   true,
	}

	// Verify all expected files exist
	for path := range expectedFiles {
		content, err := fs.ReadFile(path)
		if err != nil {
			t.Errorf("Expected file %q not found: %v", path, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("File %q has empty content", path)
		}
	}

	// All expected files verified above - test passes if we got here
	t.Logf("✓ All %d expected files found and validated", len(expectedFiles))
}

// TestSessionFixtureBuilder_FluentAPI validates the fluent builder API works correctly.
func TestSessionFixtureBuilder_FluentAPI(t *testing.T) {
	fs := NewSessionFixtureBuilder().
		AddMigration("001_test.sql", "SELECT 1;").
		AddSetup("helpers.sql", "CREATE FUNCTION test();").
		AddTestDirectory("__test__", func(t *TestDirBuilder) {
			t.AddSetup("CREATE TEMP TABLE x;")
			t.AddTest("test_001.sql", "SELECT * FROM x;")
		}).
		Build()

	// Verify files exist
	assertFileExists(t, fs, "deploy.sql")
	assertFileExists(t, fs, "migrations/001_test.sql")
	assertFileExists(t, fs, "setup/helpers.sql")
	assertFileExists(t, fs, "__test__/_setup.sql")
	assertFileExists(t, fs, "__test__/test_001.sql")
}

// TestSessionFixtureBuilder_NestedDirectories validates nested test directories work correctly.
func TestSessionFixtureBuilder_NestedDirectories(t *testing.T) {
	fs := NewSessionFixtureBuilder().
		AddTestDirectory("__test__", func(root *TestDirBuilder) {
			root.AddTest("test_root.sql", "SELECT 0;")
			root.AddSubDirectory("level1", func(l1 *TestDirBuilder) {
				l1.AddTest("test_l1.sql", "SELECT 1;")
				l1.AddSubDirectory("level2", func(l2 *TestDirBuilder) {
					l2.AddTest("test_l2.sql", "SELECT 2;")
				})
			})
		}).
		Build()

	// Verify nested structure
	assertFileExists(t, fs, "__test__/test_root.sql")
	assertFileExists(t, fs, "__test__/level1/test_l1.sql")
	assertFileExists(t, fs, "__test__/level1/level2/test_l2.sql")
}

// TestSessionFixtureBuilder_EmptyProject validates empty project fixture.
func TestSessionFixtureBuilder_EmptyProject(t *testing.T) {
	fs := EmptyProject()

	// Should only have deploy.sql
	assertFileExists(t, fs, "deploy.sql")

	// Should not have migrations
	_, err := fs.ReadFile("migrations/001.sql")
	if err == nil {
		t.Error("EmptyProject should not contain migration files")
	}
}

// TestSessionFixtureBuilder_OnlyMigrations validates migration-only fixture.
func TestSessionFixtureBuilder_OnlyMigrations(t *testing.T) {
	fs := OnlyMigrations()

	assertFileExists(t, fs, "deploy.sql")
	assertFileExists(t, fs, "migrations/001_init.sql")
	assertFileExists(t, fs, "migrations/002_indexes.sql")

	// Should have no test files
	_, err := fs.ReadFile("__test__/test_any.sql")
	if err == nil {
		t.Error("OnlyMigrations should not contain test files")
	}
}

// TestSessionFixtureBuilder_DeepNesting validates deep nesting fixture.
func TestSessionFixtureBuilder_DeepNesting(t *testing.T) {
	fs := DeepNesting()

	// Verify all 5 levels exist
	assertFileExists(t, fs, "__test__/test_level0.sql")
	assertFileExists(t, fs, "__test__/level1/test_level1.sql")
	assertFileExists(t, fs, "__test__/level1/level2/test_level2.sql")
	assertFileExists(t, fs, "__test__/level1/level2/level3/test_level3.sql")
	assertFileExists(t, fs, "__test__/level1/level2/level3/level4/test_level4.sql")
}

// Helper function to assert a file exists
func assertFileExists(t *testing.T, fs filesystem.FileSystemProvider, path string) {
	t.Helper()
	content, err := fs.ReadFile(path)
	if err != nil {
		t.Errorf("Expected file %q not found: %v", path, err)
		return
	}
	if len(content) == 0 {
		t.Errorf("File %q has empty content", path)
	}
}
