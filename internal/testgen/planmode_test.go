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

func TestPlanModeGenerator_Generate_Savepoint(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{
			SortKey:    1,
			ScriptType: "savepoint",
			BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"),
			Directory:  "./test/__test__",
		},
	}

	result := g.Generate(rows)

	// Should use pgmi_plan_command
	if !strings.Contains(result.SQL, "pgmi_plan_command") {
		t.Errorf("SQL should use pgmi_plan_command, got: %s", result.SQL)
	}
	// Should be a PERFORM statement
	if !strings.Contains(result.SQL, "PERFORM") {
		t.Errorf("SQL should use PERFORM, got: %s", result.SQL)
	}
	// Should contain the savepoint
	if !strings.Contains(result.SQL, "SAVEPOINT __pgmi_0__") {
		t.Errorf("SQL should contain savepoint, got: %s", result.SQL)
	}
}

func TestPlanModeGenerator_Generate_TestExecution(t *testing.T) {
	g := NewPlanModeGenerator()
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

	// Should wrap test file execution in pgmi_plan_command
	if !strings.Contains(result.SQL, "pgmi_plan_command") {
		t.Errorf("SQL should use pgmi_plan_command, got: %s", result.SQL)
	}
	// Should contain pgmi_execute_test_file
	if !strings.Contains(result.SQL, "pgmi_execute_test_file") {
		t.Errorf("SQL should contain pgmi_execute_test_file, got: %s", result.SQL)
	}
	// Should contain rollback as separate command
	if !strings.Contains(result.SQL, "ROLLBACK TO SAVEPOINT") {
		t.Errorf("SQL should contain ROLLBACK, got: %s", result.SQL)
	}
}

func TestPlanModeGenerator_Generate_DollarQuoting(t *testing.T) {
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

	// Should use dollar quoting for complex strings
	if !strings.Contains(result.SQL, "$__pgmi__$") {
		t.Errorf("SQL should use dollar quoting, got: %s", result.SQL)
	}
	// Original quotes should be preserved (not escaped)
	if !strings.Contains(result.SQL, "file'with'quotes.sql") {
		t.Errorf("SQL should preserve quotes within dollar quoting, got: %s", result.SQL)
	}
}

func TestPlanModeGenerator_Generate_CompleteSequence(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "fixture", Path: testdiscovery.Ptr("./test/__test__/00_fixture.sql"), Directory: "./test/__test__"},
		{SortKey: 3, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), Directory: "./test/__test__"},
		{SortKey: 4, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), AfterExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"), Directory: "./test/__test__"},
		{SortKey: 5, ScriptType: "cleanup", BeforeExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), AfterExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should have multiple PERFORM statements
	performCount := strings.Count(result.SQL, "PERFORM")
	if performCount < 5 {
		t.Errorf("SQL should have at least 5 PERFORM statements, got %d", performCount)
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

func TestPlanModeGenerator_Generate_ValidPLPGSQL(t *testing.T) {
	g := NewPlanModeGenerator()
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	lines := strings.Split(result.SQL, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Each line should be a valid PERFORM statement ending with semicolon
		if !strings.HasPrefix(line, "PERFORM ") {
			t.Errorf("Line should start with PERFORM: %s", line)
		}
		if !strings.HasSuffix(line, ";") {
			t.Errorf("Line should end with semicolon: %s", line)
		}
	}
}
