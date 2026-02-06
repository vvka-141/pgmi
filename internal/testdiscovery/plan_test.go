package testdiscovery

import (
	"testing"
)

func TestNewSavepointNamer(t *testing.T) {
	n := NewSavepointNamer()
	if n == nil {
		t.Fatal("NewSavepointNamer() returned nil")
	}
}

func TestSavepointNamer_Next(t *testing.T) {
	n := NewSavepointNamer()

	tests := []string{"__pgmi_0__", "__pgmi_1__", "__pgmi_2__"}
	for _, expected := range tests {
		got := n.Next()
		if got != expected {
			t.Errorf("Next() = %q, expected %q", got, expected)
		}
	}
}

// mockResolver returns a content resolver that returns the path as content.
func mockResolver(path string) (string, error) {
	return "-- content of " + path, nil
}

func TestNewPlanBuilder(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	if b == nil {
		t.Fatal("NewPlanBuilder() returned nil")
	}
}

func TestPlanBuilder_Build_EmptyTree(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	tree := NewTestTree()

	rows, err := b.Build(tree)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(rows) != 0 {
		t.Errorf("Expected 0 rows for empty tree, got %d", len(rows))
	}
}

func TestPlanBuilder_Build_SingleTest(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.AddTest(&TestFile{Path: "./test/__test__/01_test.sql", Filename: "01_test.sql"})
	tree.AddDirectory(dir)

	rows, err := b.Build(tree)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Expected: test with embedded content, teardown
	if len(rows) != 2 {
		t.Fatalf("Expected 2 rows, got %d", len(rows))
	}

	// First row: test execution with pre-exec savepoint
	if rows[0].StepType != "test" {
		t.Errorf("rows[0].StepType = %q, expected %q", rows[0].StepType, "test")
	}
	if rows[0].PreExec == nil || *rows[0].PreExec != "SAVEPOINT __pgmi_0__;" {
		t.Errorf("rows[0].PreExec = %v, expected SAVEPOINT __pgmi_0__;", rows[0].PreExec)
	}
	if rows[0].ScriptPath == nil || *rows[0].ScriptPath != "./test/__test__/01_test.sql" {
		t.Errorf("rows[0].ScriptPath = %v, expected ./test/__test__/01_test.sql", rows[0].ScriptPath)
	}
	if rows[0].ScriptSQL == nil {
		t.Error("rows[0].ScriptSQL should not be nil")
	}
	// Test should rollback after
	if rows[0].PostExec == nil || *rows[0].PostExec != "ROLLBACK TO SAVEPOINT __pgmi_0__;" {
		t.Errorf("rows[0].PostExec = %v, expected ROLLBACK TO SAVEPOINT __pgmi_0__;", rows[0].PostExec)
	}

	// Last row: teardown
	lastRow := rows[len(rows)-1]
	if lastRow.StepType != "teardown" {
		t.Errorf("last row StepType = %q, expected %q", lastRow.StepType, "teardown")
	}
}

func TestPlanBuilder_Build_FixtureOnly(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", Filename: "00_fixture.sql", IsFixture: true})
	tree.AddDirectory(dir)

	rows, err := b.Build(tree)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Expected: fixture, teardown
	hasFixture := false
	for _, row := range rows {
		if row.StepType == "fixture" {
			hasFixture = true
			if row.ScriptPath == nil || *row.ScriptPath != "./test/__test__/00_fixture.sql" {
				t.Errorf("fixture ScriptPath = %v, expected ./test/__test__/00_fixture.sql", row.ScriptPath)
			}
			if row.ScriptSQL == nil {
				t.Error("fixture ScriptSQL should not be nil")
			}
		}
	}
	if !hasFixture {
		t.Error("Expected fixture row in plan")
	}
}

func TestPlanBuilder_Build_FixtureAndTests(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./test/__test__/01_test.sql"})
	dir.AddTest(&TestFile{Path: "./test/__test__/02_test.sql"})
	tree.AddDirectory(dir)

	rows, err := b.Build(tree)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Verify order: fixture, test1 (rollback), test2 (rollback), teardown
	types := make([]string, len(rows))
	for i, row := range rows {
		types[i] = row.StepType
	}

	// Should have: fixture, test, test, teardown
	fixtureIdx := -1
	test1Idx := -1
	test2Idx := -1
	for i, row := range rows {
		if row.StepType == "fixture" {
			fixtureIdx = i
		}
		if row.StepType == "test" && test1Idx == -1 {
			test1Idx = i
		} else if row.StepType == "test" && test1Idx != -1 {
			test2Idx = i
		}
	}

	if fixtureIdx == -1 {
		t.Fatal("Missing fixture row")
	}
	if test1Idx == -1 || test2Idx == -1 {
		t.Fatal("Missing test rows")
	}
	if fixtureIdx >= test1Idx {
		t.Error("Fixture should come before tests")
	}

	// Tests should rollback to fixture savepoint
	if rows[test1Idx].PostExec == nil {
		t.Error("test1 should have PostExec for rollback")
	}
	if rows[test2Idx].PostExec == nil {
		t.Error("test2 should have PostExec for rollback")
	}
}

func TestPlanBuilder_Build_NestedDirectories(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	tree := NewTestTree()

	parent := NewTestDirectory("./test/__test__", 0)
	parent.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", IsFixture: true})
	parent.AddTest(&TestFile{Path: "./test/__test__/01_test.sql"})

	child := NewTestDirectory("./test/__test__/nested", 1)
	child.SetFixture(&TestFile{Path: "./test/__test__/nested/00_fixture.sql", IsFixture: true})
	child.AddTest(&TestFile{Path: "./test/__test__/nested/01_test.sql"})

	parent.AddChild(child)
	tree.AddDirectory(parent)

	rows, err := b.Build(tree)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Count different types
	fixtureCount := 0
	testCount := 0
	teardownCount := 0
	for _, row := range rows {
		switch row.StepType {
		case "fixture":
			fixtureCount++
		case "test":
			testCount++
		case "teardown":
			teardownCount++
		}
	}

	if fixtureCount != 2 {
		t.Errorf("Expected 2 fixtures, got %d", fixtureCount)
	}
	if testCount != 2 {
		t.Errorf("Expected 2 tests, got %d", testCount)
	}
	if teardownCount != 2 {
		t.Errorf("Expected 2 teardowns, got %d", teardownCount)
	}

	// Verify parent depth
	for _, row := range rows {
		if row.Directory == "./test/__test__" {
			if row.Depth != 0 {
				t.Errorf("Parent depth = %d, expected 0", row.Depth)
			}
		}
		if row.Directory == "./test/__test__/nested" {
			if row.Depth != 1 {
				t.Errorf("Child depth = %d, expected 1", row.Depth)
			}
		}
	}
}

func TestPlanBuilder_Build_MultipleDirectories(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	tree := NewTestTree()

	dir1 := NewTestDirectory("./a/__test__", 0)
	dir1.AddTest(&TestFile{Path: "./a/__test__/01_test.sql"})

	dir2 := NewTestDirectory("./b/__test__", 0)
	dir2.AddTest(&TestFile{Path: "./b/__test__/01_test.sql"})

	tree.AddDirectory(dir1)
	tree.AddDirectory(dir2)

	rows, err := b.Build(tree)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Both directories should be processed
	dirs := make(map[string]bool)
	for _, row := range rows {
		if row.Directory != "" {
			dirs[row.Directory] = true
		}
	}

	if len(dirs) != 2 {
		t.Errorf("Expected 2 directories, got %d", len(dirs))
	}
}

func TestPlanBuilder_Build_OrdinalIncreases(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./test/__test__/01_test.sql"})
	dir.AddTest(&TestFile{Path: "./test/__test__/02_test.sql"})
	tree.AddDirectory(dir)

	rows, err := b.Build(tree)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Verify Ordinal increases monotonically
	for i := 1; i < len(rows); i++ {
		if rows[i].Ordinal <= rows[i-1].Ordinal {
			t.Errorf("Ordinal not monotonically increasing: rows[%d].Ordinal=%d, rows[%d].Ordinal=%d",
				i-1, rows[i-1].Ordinal, i, rows[i].Ordinal)
		}
	}
}

func TestPlanBuilder_Build_SavepointNaming(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./test/__test__/01_test.sql"})
	tree.AddDirectory(dir)

	rows, err := b.Build(tree)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Verify savepoint names follow __pgmi_N__ convention
	for _, row := range rows {
		if row.PreExec != nil {
			cmd := *row.PreExec
			if len(cmd) > 9 && cmd[:9] == "SAVEPOINT" {
				if cmd[10:17] != "__pgmi_" {
					t.Errorf("Savepoint name doesn't follow convention: %s", cmd)
				}
			}
		}
	}
}

func TestPlanBuilder_Build_EmbeddedContent(t *testing.T) {
	b := NewPlanBuilder(mockResolver)
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./test/__test__/01_test.sql"})
	tree.AddDirectory(dir)

	rows, err := b.Build(tree)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Verify all fixture and test rows have embedded content
	for _, row := range rows {
		if row.StepType == "fixture" || row.StepType == "test" {
			if row.ScriptSQL == nil {
				t.Errorf("Row %d (%s) should have embedded ScriptSQL", row.Ordinal, row.StepType)
			}
		}
		if row.StepType == "teardown" {
			if row.ScriptSQL != nil {
				t.Errorf("Teardown row %d should not have ScriptSQL", row.Ordinal)
			}
		}
	}
}
