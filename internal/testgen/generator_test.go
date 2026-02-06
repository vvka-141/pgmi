package testgen

import (
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/sourcemap"
	"github.com/vvka-141/pgmi/internal/testdiscovery"
)

func TestNewDirectGenerator(t *testing.T) {
	g := NewDirectGenerator()
	if g == nil {
		t.Fatal("NewDirectGenerator() returned nil")
	}
}

func TestDirectGenerator_Generate_EmptyRows(t *testing.T) {
	g := NewDirectGenerator()
	result := g.Generate(nil)
	if result.SQL != "" {
		t.Errorf("Empty rows should produce empty SQL, got %q", result.SQL)
	}
	if result.SourceMap == nil {
		t.Error("SourceMap should not be nil")
	}
}

func TestDirectGenerator_Generate_SingleSavepoint(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{
			SortKey:    1,
			ScriptType: "savepoint",
			BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"),
			Directory:  "./test/__test__",
		},
	}

	result := g.Generate(rows)

	if !strings.Contains(result.SQL, "SAVEPOINT __pgmi_0__;") {
		t.Errorf("SQL should contain SAVEPOINT, got: %s", result.SQL)
	}
}

func TestDirectGenerator_Generate_TestExecution(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{
			SortKey:    1,
			ScriptType: "test",
			Path:       testdiscovery.Ptr("./test/__test__/01_test.sql"),
			AfterExec:  testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
			Directory:  "./test/__test__",
		},
	}

	result := g.Generate(rows)

	// Should call pgmi_execute_test_file
	if !strings.Contains(result.SQL, "pgmi_execute_test_file") {
		t.Errorf("SQL should call pgmi_execute_test_file, got: %s", result.SQL)
	}
	// Should contain path
	if !strings.Contains(result.SQL, "./test/__test__/01_test.sql") {
		t.Errorf("SQL should contain path, got: %s", result.SQL)
	}
	// Should have rollback after
	if !strings.Contains(result.SQL, "ROLLBACK TO SAVEPOINT") {
		t.Errorf("SQL should contain ROLLBACK, got: %s", result.SQL)
	}
}

func TestDirectGenerator_Generate_FixtureExecution(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{
			SortKey:    1,
			ScriptType: "fixture",
			Path:       testdiscovery.Ptr("./test/__test__/00_fixture.sql"),
			Directory:  "./test/__test__",
		},
	}

	result := g.Generate(rows)

	// Should call pgmi_execute_test_file for fixture too
	if !strings.Contains(result.SQL, "pgmi_execute_test_file") {
		t.Errorf("SQL should call pgmi_execute_test_file for fixture, got: %s", result.SQL)
	}
}

func TestDirectGenerator_Generate_Cleanup(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{
			SortKey:    1,
			ScriptType: "cleanup",
			BeforeExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
			AfterExec:  testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"),
			Directory:  "./test/__test__",
		},
	}

	result := g.Generate(rows)

	if !strings.Contains(result.SQL, "ROLLBACK TO SAVEPOINT __pgmi_0__;") {
		t.Errorf("SQL should contain rollback, got: %s", result.SQL)
	}
	if !strings.Contains(result.SQL, "RELEASE SAVEPOINT __pgmi_0__;") {
		t.Errorf("SQL should contain release, got: %s", result.SQL)
	}
}

func TestDirectGenerator_Generate_QuotedPath(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{
			SortKey:    1,
			ScriptType: "test",
			Path:       testdiscovery.Ptr("./test/__test__/file'with'quotes.sql"),
			Directory:  "./test/__test__",
		},
	}

	result := g.Generate(rows)

	// Single quotes should be escaped
	if !strings.Contains(result.SQL, "file''with''quotes.sql") {
		t.Errorf("SQL should escape quotes, got: %s", result.SQL)
	}
}

func TestDirectGenerator_Generate_CompleteSequence(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "fixture", Path: testdiscovery.Ptr("./test/__test__/00_fixture.sql"), Directory: "./test/__test__"},
		{SortKey: 3, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), Directory: "./test/__test__"},
		{SortKey: 4, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), AfterExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"), Directory: "./test/__test__"},
		{SortKey: 5, ScriptType: "cleanup", BeforeExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), AfterExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Verify order in output
	savepointIdx := strings.Index(result.SQL, "SAVEPOINT __pgmi_0__")
	fixtureIdx := strings.Index(result.SQL, "00_fixture.sql")
	testIdx := strings.Index(result.SQL, "01_test.sql")
	cleanupIdx := strings.Index(result.SQL, "RELEASE SAVEPOINT")

	if savepointIdx >= fixtureIdx {
		t.Error("Savepoint should come before fixture")
	}
	if fixtureIdx >= testIdx {
		t.Error("Fixture should come before test")
	}
	if testIdx >= cleanupIdx {
		t.Error("Test should come before cleanup")
	}
}

func TestDirectGenerator_Generate_SourceMap(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	if result.SourceMap == nil {
		t.Fatal("SourceMap should not be nil")
	}
	if result.SourceMap.Len() == 0 {
		t.Error("SourceMap should have entries")
	}
}

func TestDirectGenerator_Generate_SourceMap_ResolvesCorrectly(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./a/__test__"},
		{SortKey: 2, ScriptType: "test", Path: testdiscovery.Ptr("./a/__test__/01_test.sql"), Directory: "./a/__test__"},
	}

	result := g.Generate(rows)

	// Find line with test file
	lines := strings.Split(result.SQL, "\n")
	testLine := -1
	for i, line := range lines {
		if strings.Contains(line, "01_test.sql") {
			testLine = i + 1 // 1-based
			break
		}
	}

	if testLine == -1 {
		t.Fatal("Could not find test line in SQL")
	}

	file, _, desc, found := result.SourceMap.Resolve(testLine)
	if !found {
		t.Error("SourceMap should resolve test line")
	}
	if file != "./a/__test__/01_test.sql" {
		t.Errorf("SourceMap file = %q, expected ./a/__test__/01_test.sql", file)
	}
	if desc == "" {
		t.Error("SourceMap description should not be empty")
	}
}

// Helper to get SourceMap interface
func getSourceMap() *sourcemap.SourceMap {
	return sourcemap.New()
}
