package fixtures

import (
	"fmt"

	"github.com/vvka-141/pgmi/internal/files/filesystem"
)

// SessionFixtureBuilder provides a fluent API for building mock filesystem fixtures
// used in session initialization tests. It generates multi-level directory structures
// with migrations, setup files, and test files.
//
// Example usage:
//
//	fixture := NewSessionFixtureBuilder().
//	    AddMigration("001_schema.sql", "CREATE TABLE users...").
//	    AddTestDirectory("pgitest", func(t *TestDirBuilder) {
//	        t.AddSetup("CREATE TEMP TABLE test_state...")
//	        t.AddTest("test_basic.sql", "SELECT 1;")
//	    }).
//	    Build()
type SessionFixtureBuilder struct {
	files map[string]string // path -> content
}

// NewSessionFixtureBuilder creates a new fixture builder with deploy.sql pre-populated.
func NewSessionFixtureBuilder() *SessionFixtureBuilder {
	return &SessionFixtureBuilder{
		files: map[string]string{
			"deploy.sql": "-- Orchestrator script\nSELECT 1;",
		},
	}
}

// AddFile adds an arbitrary file at the specified path.
func (b *SessionFixtureBuilder) AddFile(path, content string) *SessionFixtureBuilder {
	b.files[path] = content
	return b
}

// AddMigration adds a migration file in the migrations/ directory.
func (b *SessionFixtureBuilder) AddMigration(name, content string) *SessionFixtureBuilder {
	b.files[fmt.Sprintf("migrations/%s", name)] = content
	return b
}

// AddSetup adds a setup file in the setup/ directory.
func (b *SessionFixtureBuilder) AddSetup(name, content string) *SessionFixtureBuilder {
	b.files[fmt.Sprintf("setup/%s", name)] = content
	return b
}

// AddTestDirectory adds a test directory with nested structure.
// The builder function receives a TestDirBuilder for the test directory.
//
// Example:
//
//	builder.AddTestDirectory("pgitest", func(t *TestDirBuilder) {
//	    t.AddSetup("CREATE TEMP TABLE...")
//	    t.AddTest("test_basic.sql", "SELECT 1;")
//	    t.AddSubDirectory("auth", func(sub *TestDirBuilder) {
//	        sub.AddTest("test_login.sql", "SELECT 1;")
//	    })
//	})
func (b *SessionFixtureBuilder) AddTestDirectory(name string, builderFunc func(*TestDirBuilder)) *SessionFixtureBuilder {
	testBuilder := &TestDirBuilder{
		basePath: name,
		files:    b.files,
	}
	builderFunc(testBuilder)
	return b
}

// Build generates the filesystem.FileSystemProvider from the accumulated files.
func (b *SessionFixtureBuilder) Build() filesystem.FileSystemProvider {
	fs := filesystem.NewMemoryFileSystem("/")

	for path, content := range b.files {
		fs.AddFile(path, content)
	}

	return fs
}

// TestDirBuilder builds test directory structures with support for nested subdirectories.
type TestDirBuilder struct {
	basePath string
	files    map[string]string
}

// AddSetup adds a _setup.sql file in the current test directory.
func (t *TestDirBuilder) AddSetup(content string) *TestDirBuilder {
	path := fmt.Sprintf("%s/_setup.sql", t.basePath)
	t.files[path] = content
	return t
}

// AddTest adds a test file in the current test directory.
func (t *TestDirBuilder) AddTest(name, content string) *TestDirBuilder {
	path := fmt.Sprintf("%s/%s", t.basePath, name)
	t.files[path] = content
	return t
}

// AddSubDirectory adds a nested test subdirectory.
func (t *TestDirBuilder) AddSubDirectory(name string, builderFunc func(*TestDirBuilder)) *TestDirBuilder {
	subPath := fmt.Sprintf("%s/%s", t.basePath, name)
	subBuilder := &TestDirBuilder{
		basePath: subPath,
		files:    t.files,
	}
	builderFunc(subBuilder)
	return t
}

// ============================================================================
// Pre-built Fixtures
// ============================================================================

// StandardMultiLevel creates a standard multi-level test fixture with:
// - 2 migration files
// - 1 setup file
// - 3-level deep test directory structure (__test__/auth/oauth/)
// - Setup files at multiple levels
// - Total: 7 regular files, 5 test files
func StandardMultiLevel() filesystem.FileSystemProvider {
	return NewSessionFixtureBuilder().
		// Regular files (migrations and setup)
		AddMigration("001_schema.sql", "CREATE TABLE users (id SERIAL PRIMARY KEY, username TEXT NOT NULL);").
		AddMigration("002_data.sql", "INSERT INTO users (username) VALUES ('admin'), ('user');").
		AddSetup("functions.sql", "CREATE FUNCTION get_user(id INT) RETURNS TEXT AS $$ SELECT username FROM users WHERE id = $1; $$ LANGUAGE sql;").
		// Test directory structure (3 levels deep)
		AddTestDirectory("__test__", func(t *TestDirBuilder) {
			// Level 0: Root test directory
			t.AddSetup("CREATE TEMP TABLE test_state (key TEXT, value TEXT);")
			t.AddTest("test_basic.sql", "INSERT INTO test_state VALUES ('test', 'basic');")

			// Level 1: Auth subdirectory
			t.AddSubDirectory("auth", func(auth *TestDirBuilder) {
				auth.AddSetup("CREATE TEMP TABLE auth_state (user_id INT);")
				auth.AddTest("test_login.sql", "INSERT INTO auth_state VALUES (1);")

				// Level 2: OAuth subdirectory
				auth.AddSubDirectory("oauth", func(oauth *TestDirBuilder) {
					oauth.AddTest("test_google.sql", "INSERT INTO auth_state VALUES (2);")
				})
			})

			// Level 1: Billing subdirectory
			t.AddSubDirectory("billing", func(billing *TestDirBuilder) {
				billing.AddTest("test_stripe.sql", "INSERT INTO test_state VALUES ('billing', 'stripe');")
			})
		}).
		Build()
}

// EmptyProject creates a minimal fixture with only deploy.sql.
func EmptyProject() filesystem.FileSystemProvider {
	return NewSessionFixtureBuilder().Build()
}

// OnlyMigrations creates a fixture with only migration files (no tests).
func OnlyMigrations() filesystem.FileSystemProvider {
	return NewSessionFixtureBuilder().
		AddMigration("001_init.sql", "CREATE TABLE products (id SERIAL PRIMARY KEY);").
		AddMigration("002_indexes.sql", "CREATE INDEX idx_products_id ON products(id);").
		Build()
}

// DeepNesting creates a fixture with 5 levels of test directory nesting.
func DeepNesting() filesystem.FileSystemProvider {
	return NewSessionFixtureBuilder().
		AddTestDirectory("__test__", func(t *TestDirBuilder) {
			t.AddTest("test_level0.sql", "SELECT 0;")
			t.AddSubDirectory("level1", func(l1 *TestDirBuilder) {
				l1.AddTest("test_level1.sql", "SELECT 1;")
				l1.AddSubDirectory("level2", func(l2 *TestDirBuilder) {
					l2.AddTest("test_level2.sql", "SELECT 2;")
					l2.AddSubDirectory("level3", func(l3 *TestDirBuilder) {
						l3.AddTest("test_level3.sql", "SELECT 3;")
						l3.AddSubDirectory("level4", func(l4 *TestDirBuilder) {
							l4.AddTest("test_level4.sql", "SELECT 4;")
						})
					})
				})
			})
		}).
		Build()
}

// MinimalWithTests creates a fixture with minimal structure but includes tests.
func MinimalWithTests() filesystem.FileSystemProvider {
	return NewSessionFixtureBuilder().
		AddTestDirectory("pgitest", func(t *TestDirBuilder) {
			t.AddTest("test_simple.sql", "SELECT 1 AS result;")
		}).
		Build()
}
