package testdiscovery

import (
	"testing"
)

func TestNewTestTree(t *testing.T) {
	tree := NewTestTree()
	if tree == nil {
		t.Fatal("NewTestTree() returned nil")
	}
	if !tree.IsEmpty() {
		t.Error("NewTestTree() should be empty")
	}
}

func TestNewTestDirectory(t *testing.T) {
	dir := NewTestDirectory("./users/__test__", 0)
	if dir == nil {
		t.Fatal("NewTestDirectory() returned nil")
	}
	if dir.Path != "./users/__test__" {
		t.Errorf("Path = %q, expected %q", dir.Path, "./users/__test__")
	}
	if dir.Depth != 0 {
		t.Errorf("Depth = %d, expected 0", dir.Depth)
	}
	if dir.HasFixture() {
		t.Error("HasFixture() should be false for new directory")
	}
}

func TestTestTree_AddDirectory(t *testing.T) {
	tree := NewTestTree()
	dir := NewTestDirectory("./test/__test__", 0)

	tree.AddDirectory(dir)

	if tree.IsEmpty() {
		t.Error("Tree should not be empty after AddDirectory")
	}
	if len(tree.Directories) != 1 {
		t.Errorf("Directories count = %d, expected 1", len(tree.Directories))
	}
}

func TestTestDirectory_AddTest(t *testing.T) {
	dir := NewTestDirectory("./test/__test__", 0)
	file := &TestFile{
		Path:      "./test/__test__/01_test.sql",
		Filename:  "01_test.sql",
		Directory: "./test/__test__/",
		IsFixture: false,
	}

	dir.AddTest(file)

	if len(dir.Tests) != 1 {
		t.Errorf("Tests count = %d, expected 1", len(dir.Tests))
	}
}

func TestTestDirectory_SetFixture(t *testing.T) {
	dir := NewTestDirectory("./test/__test__", 0)
	fixture := &TestFile{
		Path:      "./test/__test__/00_fixture.sql",
		Filename:  "00_fixture.sql",
		Directory: "./test/__test__/",
		IsFixture: true,
	}

	dir.SetFixture(fixture)

	if !dir.HasFixture() {
		t.Error("HasFixture() should be true after SetFixture")
	}
	if dir.Fixture != fixture {
		t.Error("Fixture not set correctly")
	}
}

func TestTestDirectory_AllFiles(t *testing.T) {
	dir := NewTestDirectory("./test/__test__", 0)

	fixture := &TestFile{Path: "fixture.sql", IsFixture: true}
	test1 := &TestFile{Path: "test1.sql", IsFixture: false}
	test2 := &TestFile{Path: "test2.sql", IsFixture: false}

	dir.SetFixture(fixture)
	dir.AddTest(test1)
	dir.AddTest(test2)

	files := dir.AllFiles()

	if len(files) != 3 {
		t.Fatalf("AllFiles() returned %d files, expected 3", len(files))
	}
	if files[0] != fixture {
		t.Error("First file should be fixture")
	}
	if files[1] != test1 {
		t.Error("Second file should be test1")
	}
	if files[2] != test2 {
		t.Error("Third file should be test2")
	}
}

func TestTestDirectory_AllFiles_NoFixture(t *testing.T) {
	dir := NewTestDirectory("./test/__test__", 0)

	test1 := &TestFile{Path: "test1.sql", IsFixture: false}
	test2 := &TestFile{Path: "test2.sql", IsFixture: false}

	dir.AddTest(test1)
	dir.AddTest(test2)

	files := dir.AllFiles()

	if len(files) != 2 {
		t.Fatalf("AllFiles() returned %d files, expected 2", len(files))
	}
}

func TestTestTree_TotalTests(t *testing.T) {
	tree := NewTestTree()

	dir1 := NewTestDirectory("./a/__test__", 0)
	dir1.AddTest(&TestFile{Path: "test1.sql"})
	dir1.AddTest(&TestFile{Path: "test2.sql"})

	dir2 := NewTestDirectory("./b/__test__", 0)
	dir2.AddTest(&TestFile{Path: "test3.sql"})

	tree.AddDirectory(dir1)
	tree.AddDirectory(dir2)

	if tree.TotalTests() != 3 {
		t.Errorf("TotalTests() = %d, expected 3", tree.TotalTests())
	}
}

func TestTestTree_TotalFixtures(t *testing.T) {
	tree := NewTestTree()

	dir1 := NewTestDirectory("./a/__test__", 0)
	dir1.SetFixture(&TestFile{Path: "fixture1.sql", IsFixture: true})

	dir2 := NewTestDirectory("./b/__test__", 0)
	// No fixture

	dir3 := NewTestDirectory("./c/__test__", 0)
	dir3.SetFixture(&TestFile{Path: "fixture3.sql", IsFixture: true})

	tree.AddDirectory(dir1)
	tree.AddDirectory(dir2)
	tree.AddDirectory(dir3)

	if tree.TotalFixtures() != 2 {
		t.Errorf("TotalFixtures() = %d, expected 2", tree.TotalFixtures())
	}
}

func TestTestDirectory_NestedCounts(t *testing.T) {
	parent := NewTestDirectory("./a/__test__", 0)
	parent.SetFixture(&TestFile{Path: "parent_fixture.sql", IsFixture: true})
	parent.AddTest(&TestFile{Path: "parent_test.sql"})

	child := NewTestDirectory("./a/__test__/nested", 1)
	child.SetFixture(&TestFile{Path: "child_fixture.sql", IsFixture: true})
	child.AddTest(&TestFile{Path: "child_test1.sql"})
	child.AddTest(&TestFile{Path: "child_test2.sql"})

	parent.AddChild(child)

	// Parent should count its own + children
	if parent.TotalTests() != 3 {
		t.Errorf("TotalTests() = %d, expected 3", parent.TotalTests())
	}
	if parent.TotalFixtures() != 2 {
		t.Errorf("TotalFixtures() = %d, expected 2", parent.TotalFixtures())
	}
}

func TestPtr(t *testing.T) {
	s := "hello"
	p := Ptr(s)
	if p == nil {
		t.Fatal("Ptr() returned nil")
	}
	if *p != s {
		t.Errorf("*Ptr() = %q, expected %q", *p, s)
	}
}
