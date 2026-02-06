package testdiscovery

import (
	"strings"
	"testing"
)

// Integration test: Filter + Build produces correct savepoint structure
func TestFilterThenBuild_SavepointsAreSequential(t *testing.T) {
	// Create tree with two top-level directories
	tree := NewTestTree()

	dirA := NewTestDirectory("./a/__test__", 0)
	dirA.SetFixture(&TestFile{Path: "./a/__test__/_setup.sql", IsFixture: true})
	dirA.AddTest(&TestFile{Path: "./a/__test__/01_test.sql"})
	tree.AddDirectory(dirA)

	dirB := NewTestDirectory("./b/__test__", 0)
	dirB.SetFixture(&TestFile{Path: "./b/__test__/_setup.sql", IsFixture: true})
	dirB.AddTest(&TestFile{Path: "./b/__test__/01_test.sql"})
	tree.AddDirectory(dirB)

	// Filter to only ./b/**
	filtered := tree.FilterByPattern("./b/**")

	// Build plan from filtered tree
	resolver := func(path string) (string, error) {
		return "-- content of " + path, nil
	}
	builder := NewPlanBuilder(resolver)
	rows, err := builder.Build(filtered)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	// Savepoints should start at 0 (not skip numbers)
	hasSavepoint0 := false
	for _, row := range rows {
		if row.PreExec != nil && strings.Contains(*row.PreExec, "__pgmi_0__") {
			hasSavepoint0 = true
		}
	}
	if !hasSavepoint0 {
		t.Error("Filtered plan should use __pgmi_0__ (sequential from start)")
	}

	// Should NOT have savepoints for ./a (filtered out)
	for _, row := range rows {
		if row.Directory == "./a/__test__" {
			t.Errorf("Filtered plan should not contain ./a/__test__")
		}
	}
}

func TestFilterThenBuild_NestedFixturesCorrect(t *testing.T) {
	// Structure:
	// ./a/__test__/
	//   _setup.sql
	//   nested/
	//     _setup.sql
	//     01_test.sql

	tree := NewTestTree()

	parent := NewTestDirectory("./a/__test__", 0)
	parent.SetFixture(&TestFile{Path: "./a/__test__/_setup.sql", IsFixture: true})

	child := NewTestDirectory("./a/__test__/nested", 1)
	child.SetFixture(&TestFile{Path: "./a/__test__/nested/_setup.sql", IsFixture: true})
	child.AddTest(&TestFile{Path: "./a/__test__/nested/01_test.sql"})

	parent.AddChild(child)
	tree.AddDirectory(parent)

	// Filter for nested only
	filtered := tree.FilterByPattern("**/nested/**")

	resolver := func(path string) (string, error) {
		return "-- " + path, nil
	}
	builder := NewPlanBuilder(resolver)
	rows, err := builder.Build(filtered)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	// Should have both fixtures (parent is ancestor)
	fixtureCount := 0
	for _, row := range rows {
		if row.StepType == "fixture" {
			fixtureCount++
		}
	}
	if fixtureCount != 2 {
		t.Errorf("Expected 2 fixtures (parent + child), got %d", fixtureCount)
	}

	// Should have corresponding teardowns
	teardownCount := 0
	for _, row := range rows {
		if row.StepType == "teardown" {
			teardownCount++
		}
	}
	if teardownCount != 2 {
		t.Errorf("Expected 2 teardowns, got %d", teardownCount)
	}
}

func TestFilterThenBuild_OnlyMatchingTestsExecuted(t *testing.T) {
	tree := NewTestTree()

	dir := NewTestDirectory("./a/__test__", 0)
	dir.AddTest(&TestFile{Path: "./a/__test__/user_test.sql"})
	dir.AddTest(&TestFile{Path: "./a/__test__/admin_test.sql"})
	dir.AddTest(&TestFile{Path: "./a/__test__/guest_test.sql"})
	tree.AddDirectory(dir)

	// Filter for user tests only
	filtered := tree.FilterByPattern("**/user_*")

	resolver := func(path string) (string, error) {
		return "-- " + path, nil
	}
	builder := NewPlanBuilder(resolver)
	rows, err := builder.Build(filtered)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	// Should have only 1 test
	testCount := 0
	for _, row := range rows {
		if row.StepType == "test" {
			testCount++
			if row.ScriptPath == nil || *row.ScriptPath != "./a/__test__/user_test.sql" {
				t.Errorf("Wrong test in plan: %v", row.ScriptPath)
			}
		}
	}
	if testCount != 1 {
		t.Errorf("Expected 1 test, got %d", testCount)
	}
}

func TestFilterThenBuild_EmptyFilterReturnsAll(t *testing.T) {
	tree := NewTestTree()

	dir := NewTestDirectory("./a/__test__", 0)
	dir.AddTest(&TestFile{Path: "./a/__test__/01_test.sql"})
	dir.AddTest(&TestFile{Path: "./a/__test__/02_test.sql"})
	tree.AddDirectory(dir)

	// Empty filter
	filtered := tree.FilterByPattern("")

	resolver := func(path string) (string, error) {
		return "-- " + path, nil
	}
	builder := NewPlanBuilder(resolver)
	rows, err := builder.Build(filtered)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	testCount := 0
	for _, row := range rows {
		if row.StepType == "test" {
			testCount++
		}
	}
	if testCount != 2 {
		t.Errorf("Expected 2 tests with empty filter, got %d", testCount)
	}
}

func TestFilterThenBuild_SavepointRollbackConsistency(t *testing.T) {
	// Verify that entry savepoints are properly released.
	// Post-fixture savepoints are implicitly destroyed when we rollback to entry savepoint.
	tree := NewTestTree()

	dir := NewTestDirectory("./a/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./a/__test__/_setup.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./a/__test__/01_test.sql"})
	tree.AddDirectory(dir)

	filtered := tree.FilterByPattern("./a/**")

	resolver := func(path string) (string, error) {
		return "-- " + path, nil
	}
	builder := NewPlanBuilder(resolver)
	rows, err := builder.Build(filtered)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	// Verify we have teardown that rolls back and releases
	hasTeardown := false
	for _, row := range rows {
		if row.StepType == "teardown" {
			hasTeardown = true
			if row.PreExec == nil || !strings.Contains(*row.PreExec, "ROLLBACK TO SAVEPOINT") {
				t.Error("Teardown should have ROLLBACK TO SAVEPOINT in PreExec")
			}
			if row.PostExec == nil || !strings.Contains(*row.PostExec, "RELEASE SAVEPOINT") {
				t.Error("Teardown should have RELEASE SAVEPOINT in PostExec")
			}
		}
	}
	if !hasTeardown {
		t.Error("Should have teardown row")
	}

	// Verify test has rollback
	for _, row := range rows {
		if row.StepType == "test" {
			if row.PostExec == nil || !strings.Contains(*row.PostExec, "ROLLBACK TO SAVEPOINT") {
				t.Error("Test should have ROLLBACK TO SAVEPOINT in PostExec")
			}
		}
	}
}

func extractSavepointName(sql string) string {
	// Extract savepoint name from "SAVEPOINT xxx;" or "ROLLBACK TO SAVEPOINT xxx;" etc.
	sql = strings.TrimSuffix(sql, ";")
	parts := strings.Fields(sql)
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
