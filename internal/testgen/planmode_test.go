package testgen

import (
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testdiscovery"
)

func TestNewPlanModeGenerator(t *testing.T) {
	g := NewPlanModeGenerator()
	if g == nil {
		t.Fatal("NewPlanModeGenerator() returned nil")
	}
}

func TestPlanModeGenerator_Generate_EmptyRows(t *testing.T) {
	g := NewPlanModeGenerator()
	result := g.Generate(nil)
	if result.SQL != "" {
		t.Errorf("Empty rows should produce empty SQL, got %q", result.SQL)
	}
	if result.SourceMap == nil {
		t.Error("SourceMap should not be nil")
	}
}

func TestPlanModeGenerator_Generate_SingleCommand(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), AfterExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should have exactly ONE PERFORM statement
	performCount := strings.Count(result.SQL, "PERFORM")
	if performCount != 1 {
		t.Errorf("Should have exactly 1 PERFORM statement, got %d\nSQL: %s", performCount, result.SQL)
	}

	// Should use pgmi_plan_command
	if !strings.Contains(result.SQL, "pgmi_plan_command") {
		t.Errorf("SQL should use pgmi_plan_command, got: %s", result.SQL)
	}

	// Should use dollar quoting
	if !strings.Contains(result.SQL, "$__pgmi__$") {
		t.Errorf("SQL should use dollar quoting, got: %s", result.SQL)
	}

	// Inner SQL should contain savepoint
	if !strings.Contains(result.SQL, "SAVEPOINT __pgmi_0__") {
		t.Errorf("SQL should contain savepoint, got: %s", result.SQL)
	}

	// Inner SQL should contain test file execution
	if !strings.Contains(result.SQL, "pgmi_execute_test_file") {
		t.Errorf("SQL should contain pgmi_execute_test_file, got: %s", result.SQL)
	}

	// Inner SQL should contain rollback
	if !strings.Contains(result.SQL, "ROLLBACK TO SAVEPOINT") {
		t.Errorf("SQL should contain ROLLBACK, got: %s", result.SQL)
	}
}

func TestPlanModeGenerator_Generate_Structure(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "fixture", Path: testdiscovery.Ptr("./test/__test__/_setup.sql"), Directory: "./test/__test__"},
		{SortKey: 3, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), AfterExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should start with PERFORM
	if !strings.HasPrefix(result.SQL, "PERFORM pg_temp.pgmi_plan_command($__pgmi__$") {
		t.Errorf("SQL should start with PERFORM wrapper, got: %s", result.SQL)
	}

	// Should end with closing
	if !strings.HasSuffix(result.SQL, "$__pgmi__$);") {
		t.Errorf("SQL should end with $__pgmi__$);, got: %s", result.SQL)
	}
}

func TestPlanModeGenerator_Generate_PreservesQuotesInPaths(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{
			SortKey:    1,
			ScriptType: "test",
			Path:       testdiscovery.Ptr("./test/__test__/file'with'quotes.sql"),
			Directory:  "./test/__test__",
		},
	}

	result := g.Generate(rows)

	// Dollar quoting means inner quotes are escaped via EscapeSQLString
	if !strings.Contains(result.SQL, "file''with''quotes.sql") {
		t.Errorf("SQL should escape single quotes in paths, got: %s", result.SQL)
	}
}

func TestPlanModeGenerator_Generate_CompleteSequence(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "fixture", Path: testdiscovery.Ptr("./test/__test__/_setup.sql"), Directory: "./test/__test__"},
		{SortKey: 3, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), Directory: "./test/__test__"},
		{SortKey: 4, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), AfterExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"), Directory: "./test/__test__"},
		{SortKey: 5, ScriptType: "cleanup", BeforeExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), AfterExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should have exactly ONE PERFORM statement wrapping everything
	performCount := strings.Count(result.SQL, "PERFORM")
	if performCount != 1 {
		t.Errorf("Should have exactly 1 PERFORM statement, got %d", performCount)
	}

	// All elements should be inside the single command
	expectedElements := []string{
		"SAVEPOINT __pgmi_0__",
		"_setup.sql",
		"SAVEPOINT __pgmi_1__",
		"01_test.sql",
		"ROLLBACK TO SAVEPOINT __pgmi_1__",
		"ROLLBACK TO SAVEPOINT __pgmi_0__",
		"RELEASE SAVEPOINT __pgmi_0__",
	}
	for _, elem := range expectedElements {
		if !strings.Contains(result.SQL, elem) {
			t.Errorf("SQL should contain %q, got: %s", elem, result.SQL)
		}
	}
}

func TestPlanModeGenerator_Generate_SourceMap(t *testing.T) {
	g := NewPlanModeGenerator()
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

func TestPlanModeGenerator_Generate_SourceMapLineOffset(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// The test file execution should be at line 2 (line 1 is PERFORM wrapper)
	// Check that source map entry exists for the test file
	entries := result.SourceMap.Entries()
	if len(entries) == 0 {
		t.Fatal("SourceMap should have entries")
	}

	// First entry should be for the test file at line 2
	entry := entries[0]
	if entry.ExpandedStart != 2 {
		t.Errorf("Source map entry should start at line 2, got %d", entry.ExpandedStart)
	}
}

func TestPlanModeGenerator_Generate_ValidPLPGSQL(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should be a single valid PERFORM statement
	if !strings.HasPrefix(result.SQL, "PERFORM ") {
		t.Errorf("SQL should start with PERFORM: %s", result.SQL)
	}
	if !strings.HasSuffix(result.SQL, ";") {
		t.Errorf("SQL should end with semicolon: %s", result.SQL)
	}

	// Should have exactly one semicolon at the end (the PERFORM statement's semicolon)
	// Inner SQL statements have their own semicolons but those are inside the dollar-quoted string
	trimmed := strings.TrimSuffix(result.SQL, ";")
	if strings.HasSuffix(trimmed, ";") {
		// This would mean double semicolon at the end, which is wrong
		t.Errorf("SQL should not have double semicolon at end: %s", result.SQL)
	}
}
