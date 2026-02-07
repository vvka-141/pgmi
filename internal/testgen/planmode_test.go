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
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should have exactly 3 PERFORM statements: BEGIN, test SQL, COMMIT
	performCount := strings.Count(result.SQL, "PERFORM")
	if performCount != 3 {
		t.Errorf("Should have exactly 3 PERFORM statements, got %d\nSQL: %s", performCount, result.SQL)
	}

	// Should schedule BEGIN
	if !strings.Contains(result.SQL, "pgmi_plan_command('BEGIN;')") {
		t.Errorf("SQL should schedule BEGIN, got: %s", result.SQL)
	}

	// Should schedule COMMIT
	if !strings.Contains(result.SQL, "pgmi_plan_command('COMMIT;')") {
		t.Errorf("SQL should schedule COMMIT, got: %s", result.SQL)
	}

	// Should use pgmi_plan_command
	if !strings.Contains(result.SQL, "pgmi_plan_command") {
		t.Errorf("SQL should use pgmi_plan_command, got: %s", result.SQL)
	}

	// Should use dollar quoting for test SQL
	if !strings.Contains(result.SQL, "$__pgmi__$") {
		t.Errorf("SQL should use dollar quoting, got: %s", result.SQL)
	}

	// Inner SQL should contain savepoint
	if !strings.Contains(result.SQL, "SAVEPOINT __pgmi_0__") {
		t.Errorf("SQL should contain savepoint, got: %s", result.SQL)
	}

	// Inner SQL should contain embedded test content
	if !strings.Contains(result.SQL, "SELECT 1;") {
		t.Errorf("SQL should contain embedded content, got: %s", result.SQL)
	}

	// Inner SQL should contain rollback
	if !strings.Contains(result.SQL, "ROLLBACK TO SAVEPOINT") {
		t.Errorf("SQL should contain ROLLBACK, got: %s", result.SQL)
	}
}

func TestPlanModeGenerator_Generate_Structure(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: testdiscovery.Ptr("./test/__test__/_setup.sql"), ScriptSQL: testdiscovery.Ptr("-- setup"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("-- test"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should start with BEGIN command
	if !strings.HasPrefix(result.SQL, "PERFORM pg_temp.pgmi_plan_command('BEGIN;');") {
		t.Errorf("SQL should start with BEGIN command, got: %s", result.SQL)
	}

	// Should end with COMMIT command
	if !strings.HasSuffix(result.SQL, "PERFORM pg_temp.pgmi_plan_command('COMMIT;');") {
		t.Errorf("SQL should end with COMMIT command, got: %s", result.SQL)
	}
}

func TestPlanModeGenerator_Generate_CompleteSequence(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: testdiscovery.Ptr("./test/__test__/_setup.sql"), ScriptSQL: testdiscovery.Ptr("-- fixture content"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("-- test content"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"), Directory: "./test/__test__"},
		{Ordinal: 3, StepType: "teardown", PreExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should have exactly 3 PERFORM statements: BEGIN, test SQL, COMMIT
	performCount := strings.Count(result.SQL, "PERFORM")
	if performCount != 3 {
		t.Errorf("Should have exactly 3 PERFORM statements, got %d", performCount)
	}

	// All elements should be inside the middle command
	expectedElements := []string{
		"SAVEPOINT __pgmi_0__",
		"-- fixture content",
		"SAVEPOINT __pgmi_1__",
		"-- test content",
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
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
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
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Check that source map entry exists for the test file
	entries := result.SourceMap.Entries()
	if len(entries) == 0 {
		t.Fatal("SourceMap should have entries")
	}

	// Entry should be for the test file at line 3
	// Line 1: BEGIN command, Line 2: PERFORM wrapper, Line 3: inner SQL start
	entry := entries[0]
	if entry.ExpandedStart != 3 {
		t.Errorf("Source map entry should start at line 3, got %d", entry.ExpandedStart)
	}
}

func TestPlanModeGenerator_Generate_ValidPLPGSQL(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should start with BEGIN command
	if !strings.HasPrefix(result.SQL, "PERFORM pg_temp.pgmi_plan_command('BEGIN;');") {
		t.Errorf("SQL should start with BEGIN command: %s", result.SQL)
	}

	// Should end with COMMIT command
	if !strings.HasSuffix(result.SQL, "PERFORM pg_temp.pgmi_plan_command('COMMIT;');") {
		t.Errorf("SQL should end with COMMIT command: %s", result.SQL)
	}

	// Should have exactly 3 PERFORM statements
	performCount := strings.Count(result.SQL, "PERFORM pg_temp.pgmi_plan_command")
	if performCount != 3 {
		t.Errorf("Should have 3 PERFORM statements, got %d", performCount)
	}
}

func TestPlanModeGenerator_Generate_MultilineContent(t *testing.T) {
	g := NewPlanModeGenerator()
	multilineSQL := "SELECT 1;\nSELECT 2;\nSELECT 3;"
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr(multilineSQL), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should contain all lines of embedded content
	if !strings.Contains(result.SQL, "SELECT 1;") {
		t.Error("SQL should contain SELECT 1;")
	}
	if !strings.Contains(result.SQL, "SELECT 2;") {
		t.Error("SQL should contain SELECT 2;")
	}
	if !strings.Contains(result.SQL, "SELECT 3;") {
		t.Error("SQL should contain SELECT 3;")
	}
}

func TestPlanModeGenerator_GenerateWithCallback_EmptyCallback(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.GenerateWithCallback(rows, "")

	// Empty callback = no callback invocations
	if strings.Contains(result.SQL, "pgmi_test_event") {
		t.Error("Empty callback should not produce callback invocations")
	}

	// Should still have test content
	if !strings.Contains(result.SQL, "SELECT 1;") {
		t.Error("SQL should contain test content")
	}
}

func TestPlanModeGenerator_GenerateWithCallback_WithCallback(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: testdiscovery.Ptr("./t/_setup.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./t/", Depth: 0},
		{Ordinal: 2, StepType: "test", ScriptPath: testdiscovery.Ptr("./t/test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 2;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"), Directory: "./t/", Depth: 0},
		{Ordinal: 3, StepType: "teardown", PreExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./t/", Depth: 0},
	}

	result := g.GenerateWithCallback(rows, "pg_temp.observer")

	// Callbacks should be inside the dollar-quoted block
	if !strings.Contains(result.SQL, "ROW('suite_start'") {
		t.Errorf("Should contain suite_start callback, got: %s", result.SQL)
	}
	if !strings.Contains(result.SQL, "ROW('fixture_start'") {
		t.Errorf("Should contain fixture_start callback, got: %s", result.SQL)
	}
	if !strings.Contains(result.SQL, "ROW('test_start'") {
		t.Errorf("Should contain test_start callback, got: %s", result.SQL)
	}
	if !strings.Contains(result.SQL, "ROW('suite_end'") {
		t.Errorf("Should contain suite_end callback, got: %s", result.SQL)
	}
	if !strings.Contains(result.SQL, "pg_temp.observer") {
		t.Errorf("Should use the provided callback function, got: %s", result.SQL)
	}
}

func TestPlanModeGenerator_GenerateWithCallback_EmptyRows(t *testing.T) {
	g := NewPlanModeGenerator()
	result := g.GenerateWithCallback(nil, "pg_temp.cb")

	if result.SQL != "" {
		t.Errorf("Empty rows should produce empty SQL, got %q", result.SQL)
	}
}

func TestPlanModeGenerator_Generate_DelegatesToGenerateWithCallback(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should produce same output as GenerateWithCallback with empty callback
	resultWithEmpty := g.GenerateWithCallback(rows, "")

	if result.SQL != resultWithEmpty.SQL {
		t.Errorf("Generate() should produce same SQL as GenerateWithCallback with empty callback\nGenerate: %s\nGenerateWithCallback: %s", result.SQL, resultWithEmpty.SQL)
	}
}
