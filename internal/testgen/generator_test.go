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
			Ordinal:   1,
			StepType:  "teardown",
			PreExec:   testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
			PostExec:  testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"),
			Directory: "./test/__test__",
		},
	}

	result := g.Generate(rows)

	if !strings.Contains(result.SQL, "ROLLBACK TO SAVEPOINT __pgmi_0__;") {
		t.Errorf("SQL should contain ROLLBACK, got: %s", result.SQL)
	}
}

func TestDirectGenerator_Generate_TestExecution(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{
			Ordinal:    1,
			StepType:   "test",
			ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"),
			ScriptSQL:  testdiscovery.Ptr("SELECT 1;"),
			PreExec:    testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"),
			PostExec:   testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
			Directory:  "./test/__test__",
		},
	}

	result := g.Generate(rows)

	// Should contain embedded SQL
	if !strings.Contains(result.SQL, "SELECT 1;") {
		t.Errorf("SQL should contain embedded content, got: %s", result.SQL)
	}
	// Should have savepoint before
	if !strings.Contains(result.SQL, "SAVEPOINT __pgmi_0__") {
		t.Errorf("SQL should contain SAVEPOINT, got: %s", result.SQL)
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
			Ordinal:    1,
			StepType:   "fixture",
			ScriptPath: testdiscovery.Ptr("./test/__test__/00_fixture.sql"),
			ScriptSQL:  testdiscovery.Ptr("CREATE TABLE test_data(id int);"),
			PreExec:    testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"),
			Directory:  "./test/__test__",
		},
	}

	result := g.Generate(rows)

	// Should contain embedded fixture SQL
	if !strings.Contains(result.SQL, "CREATE TABLE test_data(id int);") {
		t.Errorf("SQL should contain embedded fixture content, got: %s", result.SQL)
	}
}

func TestDirectGenerator_Generate_Teardown(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{
			Ordinal:   1,
			StepType:  "teardown",
			PreExec:   testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
			PostExec:  testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"),
			Directory: "./test/__test__",
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

func TestDirectGenerator_Generate_CompleteSequence(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: testdiscovery.Ptr("./test/__test__/00_fixture.sql"), ScriptSQL: testdiscovery.Ptr("-- fixture"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("-- test"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"), Directory: "./test/__test__"},
		{Ordinal: 3, StepType: "teardown", PreExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Verify order in output
	savepointIdx := strings.Index(result.SQL, "SAVEPOINT __pgmi_0__")
	fixtureIdx := strings.Index(result.SQL, "-- fixture")
	testIdx := strings.Index(result.SQL, "-- test")
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

func TestDirectGenerator_Generate_SourceMap_ResolvesCorrectly(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./a/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./a/__test__"},
	}

	result := g.Generate(rows)

	// The test content should be at line 3 (after BEGIN; on line 1 and SAVEPOINT on line 2)
	file, _, desc, found := result.SourceMap.Resolve(3)
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

func TestDirectGenerator_Generate_MultilineContent(t *testing.T) {
	g := NewDirectGenerator()
	multilineSQL := "SELECT 1;\nSELECT 2;\nSELECT 3;"
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr(multilineSQL), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
	}

	result := g.Generate(rows)

	// Should contain all lines
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

// Helper to get SourceMap interface
func getSourceMap() *sourcemap.SourceMap {
	return sourcemap.New()
}

func TestDirectGenerator_GenerateWithCallback_EmptyCallback(t *testing.T) {
	g := NewDirectGenerator()
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

func TestDirectGenerator_GenerateWithCallback_WithCallback(t *testing.T) {
	g := NewDirectGenerator()
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: testdiscovery.Ptr("./t/_setup.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./t/", Depth: 0},
		{Ordinal: 2, StepType: "test", ScriptPath: testdiscovery.Ptr("./t/test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 2;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"), Directory: "./t/", Depth: 0},
		{Ordinal: 3, StepType: "teardown", PreExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./t/", Depth: 0},
	}

	result := g.GenerateWithCallback(rows, "pg_temp.observer")

	// Check for suite_start callback
	if !strings.Contains(result.SQL, "ROW('suite_start'") {
		t.Errorf("Should contain suite_start callback, got: %s", result.SQL)
	}

	// Check for fixture_start callback
	if !strings.Contains(result.SQL, "ROW('fixture_start'") {
		t.Errorf("Should contain fixture_start callback, got: %s", result.SQL)
	}

	// Check for fixture_end callback
	if !strings.Contains(result.SQL, "ROW('fixture_end'") {
		t.Errorf("Should contain fixture_end callback, got: %s", result.SQL)
	}

	// Check for test_start callback
	if !strings.Contains(result.SQL, "ROW('test_start'") {
		t.Errorf("Should contain test_start callback, got: %s", result.SQL)
	}

	// Check for test_end callback
	if !strings.Contains(result.SQL, "ROW('test_end'") {
		t.Errorf("Should contain test_end callback, got: %s", result.SQL)
	}

	// Check for rollback callback
	if !strings.Contains(result.SQL, "ROW('rollback'") {
		t.Errorf("Should contain rollback callback, got: %s", result.SQL)
	}

	// Check for teardown_start callback
	if !strings.Contains(result.SQL, "ROW('teardown_start'") {
		t.Errorf("Should contain teardown_start callback, got: %s", result.SQL)
	}

	// Check for teardown_end callback
	if !strings.Contains(result.SQL, "ROW('teardown_end'") {
		t.Errorf("Should contain teardown_end callback, got: %s", result.SQL)
	}

	// Check for suite_end callback
	if !strings.Contains(result.SQL, "ROW('suite_end'") {
		t.Errorf("Should contain suite_end callback, got: %s", result.SQL)
	}

	// Check callback function is used
	if !strings.Contains(result.SQL, "pg_temp.observer") {
		t.Errorf("Should use the provided callback function, got: %s", result.SQL)
	}

	// Check for correct type cast
	if !strings.Contains(result.SQL, "::pg_temp.pgmi_test_event") {
		t.Errorf("Should cast to pgmi_test_event type, got: %s", result.SQL)
	}
}

func TestDirectGenerator_GenerateWithCallback_EmptyRows(t *testing.T) {
	g := NewDirectGenerator()
	result := g.GenerateWithCallback(nil, "pg_temp.cb")

	if result.SQL != "" {
		t.Errorf("Empty rows should produce empty SQL, got %q", result.SQL)
	}
}

func TestDirectGenerator_Generate_DelegatesToGenerateWithCallback(t *testing.T) {
	g := NewDirectGenerator()
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
