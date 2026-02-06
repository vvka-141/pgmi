package testdiscovery

import (
	"testing"
)

func TestTestTree_FilterByPattern_EmptyPattern(t *testing.T) {
	tree := NewTestTree()
	dir := NewTestDirectory("./a/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./a/__test__/_setup.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./a/__test__/01_test.sql"})
	tree.AddDirectory(dir)

	filtered := tree.FilterByPattern("")

	if filtered.IsEmpty() {
		t.Error("Empty pattern should return non-empty tree")
	}
	if len(filtered.Directories) != 1 {
		t.Errorf("Expected 1 directory, got %d", len(filtered.Directories))
	}
	if len(filtered.Directories[0].Tests) != 1 {
		t.Errorf("Expected 1 test, got %d", len(filtered.Directories[0].Tests))
	}
}

func TestTestTree_FilterByPattern_NilPattern(t *testing.T) {
	tree := NewTestTree()
	dir := NewTestDirectory("./a/__test__", 0)
	dir.AddTest(&TestFile{Path: "./a/__test__/01_test.sql"})
	tree.AddDirectory(dir)

	// Empty string is the nil case for patterns
	filtered := tree.FilterByPattern("")

	if len(filtered.Directories) != 1 {
		t.Errorf("Nil/empty pattern should return full tree")
	}
}

func TestTestTree_FilterByPattern_MatchesSingleDirectory(t *testing.T) {
	tree := NewTestTree()

	dirA := NewTestDirectory("./a/__test__", 0)
	dirA.AddTest(&TestFile{Path: "./a/__test__/01_test.sql"})
	tree.AddDirectory(dirA)

	dirB := NewTestDirectory("./b/__test__", 0)
	dirB.AddTest(&TestFile{Path: "./b/__test__/01_test.sql"})
	tree.AddDirectory(dirB)

	filtered := tree.FilterByPattern("./a/**")

	if len(filtered.Directories) != 1 {
		t.Fatalf("Expected 1 directory, got %d", len(filtered.Directories))
	}
	if filtered.Directories[0].Path != "./a/__test__" {
		t.Errorf("Expected ./a/__test__, got %s", filtered.Directories[0].Path)
	}
}

func TestTestTree_FilterByPattern_IncludesAncestorFixtures(t *testing.T) {
	// Structure:
	// ./a/__test__/
	//   _setup.sql (fixture)
	//   nested/
	//     _setup.sql (fixture)
	//     01_test.sql

	tree := NewTestTree()
	parent := NewTestDirectory("./a/__test__", 0)
	parent.SetFixture(&TestFile{Path: "./a/__test__/_setup.sql", IsFixture: true})

	child := NewTestDirectory("./a/__test__/nested", 1)
	child.SetFixture(&TestFile{Path: "./a/__test__/nested/_setup.sql", IsFixture: true})
	child.AddTest(&TestFile{Path: "./a/__test__/nested/01_test.sql"})

	parent.AddChild(child)
	tree.AddDirectory(parent)

	// Filter for nested tests only
	filtered := tree.FilterByPattern("./a/__test__/nested/**")

	// Should include parent directory with its fixture (ancestor)
	if len(filtered.Directories) != 1 {
		t.Fatalf("Expected 1 top-level directory, got %d", len(filtered.Directories))
	}

	parentDir := filtered.Directories[0]
	if parentDir.Path != "./a/__test__" {
		t.Errorf("Expected parent path ./a/__test__, got %s", parentDir.Path)
	}
	if !parentDir.HasFixture() {
		t.Error("Parent should still have fixture (ancestor fixture)")
	}
	if len(parentDir.Tests) != 0 {
		t.Error("Parent should have no tests (not matched)")
	}

	// Should include nested directory with fixture and test
	if len(parentDir.Children) != 1 {
		t.Fatalf("Expected 1 child, got %d", len(parentDir.Children))
	}

	childDir := parentDir.Children[0]
	if childDir.Path != "./a/__test__/nested" {
		t.Errorf("Expected child path ./a/__test__/nested, got %s", childDir.Path)
	}
	if !childDir.HasFixture() {
		t.Error("Child should have fixture")
	}
	if len(childDir.Tests) != 1 {
		t.Errorf("Child should have 1 test, got %d", len(childDir.Tests))
	}
}

func TestTestTree_FilterByPattern_NoMatches(t *testing.T) {
	tree := NewTestTree()
	dir := NewTestDirectory("./a/__test__", 0)
	dir.AddTest(&TestFile{Path: "./a/__test__/01_test.sql"})
	tree.AddDirectory(dir)

	filtered := tree.FilterByPattern("./nonexistent/**")

	if !filtered.IsEmpty() {
		t.Error("No matches should return empty tree")
	}
}

func TestTestTree_FilterByPattern_PartialTestsInDirectory(t *testing.T) {
	// Only some tests in directory match pattern
	tree := NewTestTree()
	dir := NewTestDirectory("./a/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./a/__test__/_setup.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./a/__test__/user_test.sql"})
	dir.AddTest(&TestFile{Path: "./a/__test__/admin_test.sql"})
	dir.AddTest(&TestFile{Path: "./a/__test__/guest_test.sql"})
	tree.AddDirectory(dir)

	// Match only user tests
	filtered := tree.FilterByPattern("**/user_*")

	if len(filtered.Directories) != 1 {
		t.Fatalf("Expected 1 directory, got %d", len(filtered.Directories))
	}
	if len(filtered.Directories[0].Tests) != 1 {
		t.Errorf("Expected 1 matching test, got %d", len(filtered.Directories[0].Tests))
	}
	if filtered.Directories[0].Tests[0].Path != "./a/__test__/user_test.sql" {
		t.Errorf("Wrong test matched: %s", filtered.Directories[0].Tests[0].Path)
	}
	// Fixture should still be included
	if !filtered.Directories[0].HasFixture() {
		t.Error("Fixture should be preserved when any tests match")
	}
}

func TestTestTree_FilterByPattern_MultipleDirectoriesMatch(t *testing.T) {
	tree := NewTestTree()

	dirA := NewTestDirectory("./users/__test__", 0)
	dirA.AddTest(&TestFile{Path: "./users/__test__/01_test.sql"})
	tree.AddDirectory(dirA)

	dirB := NewTestDirectory("./users/__test__/admin", 0)
	dirB.AddTest(&TestFile{Path: "./users/__test__/admin/01_test.sql"})
	tree.AddDirectory(dirB)

	dirC := NewTestDirectory("./products/__test__", 0)
	dirC.AddTest(&TestFile{Path: "./products/__test__/01_test.sql"})
	tree.AddDirectory(dirC)

	// Match all user tests
	filtered := tree.FilterByPattern("./users/**")

	// Should include both user directories
	if len(filtered.Directories) != 2 {
		t.Errorf("Expected 2 directories under users, got %d", len(filtered.Directories))
	}

	// Products should be excluded
	for _, d := range filtered.Directories {
		if d.Path == "./products/__test__" {
			t.Error("Products directory should be excluded")
		}
	}
}

func TestTestTree_FilterByPattern_DeepNesting(t *testing.T) {
	// Structure:
	// ./a/__test__/
	//   _setup.sql
	//   level1/
	//     _setup.sql
	//     level2/
	//       _setup.sql
	//       01_test.sql

	tree := NewTestTree()

	root := NewTestDirectory("./a/__test__", 0)
	root.SetFixture(&TestFile{Path: "./a/__test__/_setup.sql", IsFixture: true})

	level1 := NewTestDirectory("./a/__test__/level1", 1)
	level1.SetFixture(&TestFile{Path: "./a/__test__/level1/_setup.sql", IsFixture: true})

	level2 := NewTestDirectory("./a/__test__/level1/level2", 2)
	level2.SetFixture(&TestFile{Path: "./a/__test__/level1/level2/_setup.sql", IsFixture: true})
	level2.AddTest(&TestFile{Path: "./a/__test__/level1/level2/01_test.sql"})

	level1.AddChild(level2)
	root.AddChild(level1)
	tree.AddDirectory(root)

	// Filter for deepest level
	filtered := tree.FilterByPattern("**/level2/**")

	// All ancestor fixtures should be included
	if len(filtered.Directories) != 1 {
		t.Fatalf("Expected 1 root directory, got %d", len(filtered.Directories))
	}

	rootFiltered := filtered.Directories[0]
	if !rootFiltered.HasFixture() {
		t.Error("Root should have fixture")
	}
	if len(rootFiltered.Tests) != 0 {
		t.Error("Root should have no tests")
	}

	if len(rootFiltered.Children) != 1 {
		t.Fatalf("Expected 1 child at level1")
	}
	level1Filtered := rootFiltered.Children[0]
	if !level1Filtered.HasFixture() {
		t.Error("Level1 should have fixture")
	}

	if len(level1Filtered.Children) != 1 {
		t.Fatalf("Expected 1 child at level2")
	}
	level2Filtered := level1Filtered.Children[0]
	if !level2Filtered.HasFixture() {
		t.Error("Level2 should have fixture")
	}
	if len(level2Filtered.Tests) != 1 {
		t.Error("Level2 should have 1 test")
	}
}

func TestTestTree_FilterByPattern_PreservesDepth(t *testing.T) {
	tree := NewTestTree()

	parent := NewTestDirectory("./a/__test__", 0)
	child := NewTestDirectory("./a/__test__/nested", 1)
	child.AddTest(&TestFile{Path: "./a/__test__/nested/01_test.sql"})
	parent.AddChild(child)
	tree.AddDirectory(parent)

	filtered := tree.FilterByPattern("./a/**")

	if filtered.Directories[0].Depth != 0 {
		t.Errorf("Parent depth should be 0, got %d", filtered.Directories[0].Depth)
	}
	if filtered.Directories[0].Children[0].Depth != 1 {
		t.Errorf("Child depth should be 1, got %d", filtered.Directories[0].Children[0].Depth)
	}
}

func TestTestTree_FilterByPattern_EmptyTree(t *testing.T) {
	tree := NewTestTree()

	filtered := tree.FilterByPattern("./a/**")

	if !filtered.IsEmpty() {
		t.Error("Filtering empty tree should return empty tree")
	}
}

func TestTestTree_FilterByPattern_ExactPathMatch(t *testing.T) {
	tree := NewTestTree()
	dir := NewTestDirectory("./a/__test__", 0)
	dir.AddTest(&TestFile{Path: "./a/__test__/specific_test.sql"})
	dir.AddTest(&TestFile{Path: "./a/__test__/other_test.sql"})
	tree.AddDirectory(dir)

	// Match exact file
	filtered := tree.FilterByPattern("./a/__test__/specific_test.sql")

	if len(filtered.Directories) != 1 {
		t.Fatalf("Expected 1 directory")
	}
	if len(filtered.Directories[0].Tests) != 1 {
		t.Fatalf("Expected 1 test, got %d", len(filtered.Directories[0].Tests))
	}
	if filtered.Directories[0].Tests[0].Path != "./a/__test__/specific_test.sql" {
		t.Errorf("Wrong test: %s", filtered.Directories[0].Tests[0].Path)
	}
}
