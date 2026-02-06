package preprocessor

import (
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testdiscovery"
)

func TestNewPipeline(t *testing.T) {
	p := NewPipeline(false)
	if p == nil {
		t.Fatal("NewPipeline() returned nil")
	}
}

func TestPipeline_Process_NoMacros(t *testing.T) {
	p := NewPipeline(false)

	sql := `
		SELECT 1;
		SELECT 2;
	`
	rows := []testdiscovery.TestScriptRow{}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	// Should return original SQL unchanged
	if result.ExpandedSQL != sql {
		t.Errorf("Expected original SQL, got %q", result.ExpandedSQL)
	}
	if result.MacroCount != 0 {
		t.Errorf("MacroCount = %d, expected 0", result.MacroCount)
	}
}

func TestPipeline_Process_DirectMode_SingleMacro(t *testing.T) {
	p := NewPipeline(false) // direct mode

	sql := `SELECT pgmi_test();`
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), AfterExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 3, ScriptType: "cleanup", BeforeExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), AfterExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if result.MacroCount != 1 {
		t.Errorf("MacroCount = %d, expected 1", result.MacroCount)
	}

	// Should contain savepoint commands
	if !strings.Contains(result.ExpandedSQL, "SAVEPOINT __pgmi_0__") {
		t.Errorf("ExpandedSQL should contain SAVEPOINT, got: %s", result.ExpandedSQL)
	}

	// Should contain test file execution
	if !strings.Contains(result.ExpandedSQL, "pgmi_execute_test_file") {
		t.Errorf("ExpandedSQL should contain pgmi_execute_test_file, got: %s", result.ExpandedSQL)
	}
}

func TestPipeline_Process_PlanMode_SingleMacro(t *testing.T) {
	p := NewPipeline(true) // plan mode

	sql := `PERFORM pgmi_plan_test();`
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{SortKey: 2, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), AfterExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if result.MacroCount != 1 {
		t.Errorf("MacroCount = %d, expected 1", result.MacroCount)
	}

	// Should contain PERFORM pg_temp.pgmi_plan_command
	if !strings.Contains(result.ExpandedSQL, "PERFORM pg_temp.pgmi_plan_command") {
		t.Errorf("ExpandedSQL should contain PERFORM calls, got: %s", result.ExpandedSQL)
	}
}

func TestPipeline_Process_WithPattern(t *testing.T) {
	p := NewPipeline(false)

	sql := `SELECT pgmi_test('./a/__test__/**');`
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./a/__test__"},
		{SortKey: 2, ScriptType: "test", Path: testdiscovery.Ptr("./a/__test__/01_test.sql"), Directory: "./a/__test__"},
		{SortKey: 3, ScriptType: "cleanup", Directory: "./a/__test__"},
		{SortKey: 4, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), Directory: "./b/__test__"},
		{SortKey: 5, ScriptType: "test", Path: testdiscovery.Ptr("./b/__test__/01_test.sql"), Directory: "./b/__test__"},
		{SortKey: 6, ScriptType: "cleanup", Directory: "./b/__test__"},
	}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	// Should only include ./a/__test__ tests
	if strings.Contains(result.ExpandedSQL, "./b/__test__") {
		t.Errorf("ExpandedSQL should not contain ./b/__test__ (filtered out), got: %s", result.ExpandedSQL)
	}
	if !strings.Contains(result.ExpandedSQL, "./a/__test__") {
		t.Errorf("ExpandedSQL should contain ./a/__test__, got: %s", result.ExpandedSQL)
	}
}

func TestPipeline_Process_EmptyRows(t *testing.T) {
	p := NewPipeline(false)

	sql := `SELECT pgmi_test();`
	rows := []testdiscovery.TestScriptRow{}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	// Should still process, just with empty expansion
	if result.MacroCount != 1 {
		t.Errorf("MacroCount = %d, expected 1", result.MacroCount)
	}
}

func TestPipeline_Process_SourceMap(t *testing.T) {
	p := NewPipeline(false)

	sql := `SELECT pgmi_test();`
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "test", Path: testdiscovery.Ptr("./test/__test__/01_test.sql"), Directory: "./test/__test__"},
	}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if result.SourceMap == nil {
		t.Error("SourceMap should not be nil")
	}
}

func TestPipeline_Process_MultipleMacros(t *testing.T) {
	p := NewPipeline(false)

	sql := `
		SELECT pgmi_test('./a/**');
		-- some other SQL
		SELECT pgmi_test('./b/**');
	`
	rows := []testdiscovery.TestScriptRow{
		{SortKey: 1, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./a/__test__"},
		{SortKey: 2, ScriptType: "test", Path: testdiscovery.Ptr("./a/__test__/01.sql"), Directory: "./a/__test__"},
		{SortKey: 3, ScriptType: "cleanup", Directory: "./a/__test__"},
		{SortKey: 4, ScriptType: "savepoint", BeforeExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), Directory: "./b/__test__"},
		{SortKey: 5, ScriptType: "test", Path: testdiscovery.Ptr("./b/__test__/01.sql"), Directory: "./b/__test__"},
		{SortKey: 6, ScriptType: "cleanup", Directory: "./b/__test__"},
	}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if result.MacroCount != 2 {
		t.Errorf("MacroCount = %d, expected 2", result.MacroCount)
	}
}

func TestPipeline_Process_PreservesNonMacroContent(t *testing.T) {
	p := NewPipeline(false)

	sql := `
		-- Header comment
		SELECT 1;
		SELECT pgmi_test();
		SELECT 2;
	`
	rows := []testdiscovery.TestScriptRow{}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	// Should preserve other SQL
	if !strings.Contains(result.ExpandedSQL, "SELECT 1;") {
		t.Error("Should preserve SELECT 1;")
	}
	if !strings.Contains(result.ExpandedSQL, "SELECT 2;") {
		t.Error("Should preserve SELECT 2;")
	}
}
