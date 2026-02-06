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

func TestNewPlanBuilder(t *testing.T) {
	b := NewPlanBuilder()
	if b == nil {
		t.Fatal("NewPlanBuilder() returned nil")
	}
}

func TestPlanBuilder_Build_EmptyTree(t *testing.T) {
	b := NewPlanBuilder()
	tree := NewTestTree()

	rows := b.Build(tree)

	if len(rows) != 0 {
		t.Errorf("Expected 0 rows for empty tree, got %d", len(rows))
	}
}

func TestPlanBuilder_Build_SingleTest(t *testing.T) {
	b := NewPlanBuilder()
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.AddTest(&TestFile{Path: "./test/__test__/01_test.sql", Filename: "01_test.sql"})
	tree.AddDirectory(dir)

	rows := b.Build(tree)

	// Expected: SAVEPOINT, test, ROLLBACK TO
	if len(rows) < 3 {
		t.Fatalf("Expected at least 3 rows, got %d", len(rows))
	}

	// First row: savepoint creation
	if rows[0].ScriptType != "savepoint" {
		t.Errorf("rows[0].ScriptType = %q, expected %q", rows[0].ScriptType, "savepoint")
	}
	if rows[0].BeforeExec == nil || *rows[0].BeforeExec != "SAVEPOINT __pgmi_0__;" {
		t.Errorf("rows[0].BeforeExec = %v, expected SAVEPOINT __pgmi_0__;", rows[0].BeforeExec)
	}

	// Second row: test execution
	if rows[1].ScriptType != "test" {
		t.Errorf("rows[1].ScriptType = %q, expected %q", rows[1].ScriptType, "test")
	}
	if rows[1].Path == nil || *rows[1].Path != "./test/__test__/01_test.sql" {
		t.Errorf("rows[1].Path = %v, expected ./test/__test__/01_test.sql", rows[1].Path)
	}
	// Test should rollback after
	if rows[1].AfterExec == nil || *rows[1].AfterExec != "ROLLBACK TO SAVEPOINT __pgmi_0__;" {
		t.Errorf("rows[1].AfterExec = %v, expected ROLLBACK TO SAVEPOINT __pgmi_0__;", rows[1].AfterExec)
	}

	// Last row: release savepoint
	lastRow := rows[len(rows)-1]
	if lastRow.ScriptType != "cleanup" {
		t.Errorf("last row ScriptType = %q, expected %q", lastRow.ScriptType, "cleanup")
	}
}

func TestPlanBuilder_Build_FixtureOnly(t *testing.T) {
	b := NewPlanBuilder()
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", Filename: "00_fixture.sql", IsFixture: true})
	tree.AddDirectory(dir)

	rows := b.Build(tree)

	// Expected: SAVEPOINT, fixture, RELEASE or just cleanup
	hasFixture := false
	for _, row := range rows {
		if row.ScriptType == "fixture" {
			hasFixture = true
			if row.Path == nil || *row.Path != "./test/__test__/00_fixture.sql" {
				t.Errorf("fixture Path = %v, expected ./test/__test__/00_fixture.sql", row.Path)
			}
		}
	}
	if !hasFixture {
		t.Error("Expected fixture row in plan")
	}
}

func TestPlanBuilder_Build_FixtureAndTests(t *testing.T) {
	b := NewPlanBuilder()
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./test/__test__/01_test.sql"})
	dir.AddTest(&TestFile{Path: "./test/__test__/02_test.sql"})
	tree.AddDirectory(dir)

	rows := b.Build(tree)

	// Verify order: savepoint → fixture → savepoint → test1 (rollback) → test2 (rollback) → cleanup
	types := make([]string, len(rows))
	for i, row := range rows {
		types[i] = row.ScriptType
	}

	// Should have at least: savepoint, fixture, savepoint, test, test, cleanup
	fixtureIdx := -1
	test1Idx := -1
	test2Idx := -1
	for i, row := range rows {
		if row.ScriptType == "fixture" {
			fixtureIdx = i
		}
		if row.ScriptType == "test" && test1Idx == -1 {
			test1Idx = i
		} else if row.ScriptType == "test" && test1Idx != -1 {
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
	if rows[test1Idx].AfterExec == nil {
		t.Error("test1 should have AfterExec for rollback")
	}
	if rows[test2Idx].AfterExec == nil {
		t.Error("test2 should have AfterExec for rollback")
	}
}

func TestPlanBuilder_Build_NestedDirectories(t *testing.T) {
	b := NewPlanBuilder()
	tree := NewTestTree()

	parent := NewTestDirectory("./test/__test__", 0)
	parent.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", IsFixture: true})
	parent.AddTest(&TestFile{Path: "./test/__test__/01_test.sql"})

	child := NewTestDirectory("./test/__test__/nested", 1)
	child.SetFixture(&TestFile{Path: "./test/__test__/nested/00_fixture.sql", IsFixture: true})
	child.AddTest(&TestFile{Path: "./test/__test__/nested/01_test.sql"})

	parent.AddChild(child)
	tree.AddDirectory(parent)

	rows := b.Build(tree)

	// Verify hierarchy: parent fixture, parent savepoint, parent test (rollback),
	// child (savepoint, child fixture, child savepoint, child test, rollback, cleanup),
	// cleanup

	// Count different types
	savepointCount := 0
	fixtureCount := 0
	testCount := 0
	cleanupCount := 0
	for _, row := range rows {
		switch row.ScriptType {
		case "savepoint":
			savepointCount++
		case "fixture":
			fixtureCount++
		case "test":
			testCount++
		case "cleanup":
			cleanupCount++
		}
	}

	if fixtureCount != 2 {
		t.Errorf("Expected 2 fixtures, got %d", fixtureCount)
	}
	if testCount != 2 {
		t.Errorf("Expected 2 tests, got %d", testCount)
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
	b := NewPlanBuilder()
	tree := NewTestTree()

	dir1 := NewTestDirectory("./a/__test__", 0)
	dir1.AddTest(&TestFile{Path: "./a/__test__/01_test.sql"})

	dir2 := NewTestDirectory("./b/__test__", 0)
	dir2.AddTest(&TestFile{Path: "./b/__test__/01_test.sql"})

	tree.AddDirectory(dir1)
	tree.AddDirectory(dir2)

	rows := b.Build(tree)

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

func TestPlanBuilder_Build_SortKeyIncreases(t *testing.T) {
	b := NewPlanBuilder()
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./test/__test__/01_test.sql"})
	dir.AddTest(&TestFile{Path: "./test/__test__/02_test.sql"})
	tree.AddDirectory(dir)

	rows := b.Build(tree)

	// Verify SortKey increases monotonically
	for i := 1; i < len(rows); i++ {
		if rows[i].SortKey <= rows[i-1].SortKey {
			t.Errorf("SortKey not monotonically increasing: rows[%d].SortKey=%d, rows[%d].SortKey=%d",
				i-1, rows[i-1].SortKey, i, rows[i].SortKey)
		}
	}
}

func TestPlanBuilder_Build_SavepointNaming(t *testing.T) {
	b := NewPlanBuilder()
	tree := NewTestTree()

	dir := NewTestDirectory("./test/__test__", 0)
	dir.SetFixture(&TestFile{Path: "./test/__test__/00_fixture.sql", IsFixture: true})
	dir.AddTest(&TestFile{Path: "./test/__test__/01_test.sql"})
	tree.AddDirectory(dir)

	rows := b.Build(tree)

	// Verify savepoint names follow __pgmi_N__ convention
	for _, row := range rows {
		if row.BeforeExec != nil {
			cmd := *row.BeforeExec
			if len(cmd) > 0 && cmd[:9] == "SAVEPOINT" {
				if cmd[10:17] != "__pgmi_" {
					t.Errorf("Savepoint name doesn't follow convention: %s", cmd)
				}
			}
		}
	}
}
