package testgen

import (
	"strings"
	"testing"
)

func TestNewPlanGenerator(t *testing.T) {
	g := NewPlanGenerator()
	if g == nil {
		t.Fatal("NewPlanGenerator() returned nil")
	}
}

func TestPlanGenerator_Generate_EmptyRows(t *testing.T) {
	g := NewPlanGenerator()
	result := g.Generate(nil, "")
	if result.SQL != "" {
		t.Errorf("Empty rows should produce empty SQL, got %q", result.SQL)
	}
	if result.SourceMap == nil {
		t.Error("SourceMap should not be nil")
	}
}

func TestPlanGenerator_Generate_SingleTest(t *testing.T) {
	g := NewPlanGenerator()
	rows := []PlanRow{
		{
			Ordinal:    1,
			StepType:   StepTypeTest,
			ScriptPath: ptr("./test/__test__/01_test.sql"),
			Directory:  "./test/__test__/",
			Depth:      0,
		},
	}

	result := g.Generate(rows, "")

	// Should NOT contain BEGIN/COMMIT (caller controls transaction)
	if strings.Contains(result.SQL, "BEGIN;") {
		t.Errorf("SQL should not contain BEGIN (caller controls transaction), got: %s", result.SQL)
	}
	if strings.Contains(result.SQL, "COMMIT;") {
		t.Errorf("SQL should not contain COMMIT (caller controls transaction), got: %s", result.SQL)
	}

	// Should contain EXECUTE to fetch content from pgmi_test_source
	if !strings.Contains(result.SQL, "EXECUTE (SELECT content FROM pg_temp._pgmi_test_source") {
		t.Errorf("SQL should contain EXECUTE with content fetch, got: %s", result.SQL)
	}

	// Should contain the script path in the query
	if !strings.Contains(result.SQL, "./test/__test__/01_test.sql") {
		t.Errorf("SQL should contain script path, got: %s", result.SQL)
	}

	// Should have savepoint and rollback
	if !strings.Contains(result.SQL, "SAVEPOINT") {
		t.Errorf("SQL should contain SAVEPOINT, got: %s", result.SQL)
	}
	if !strings.Contains(result.SQL, "ROLLBACK TO SAVEPOINT") {
		t.Errorf("SQL should contain ROLLBACK TO SAVEPOINT, got: %s", result.SQL)
	}
}

func TestPlanGenerator_Generate_FixtureAndTest(t *testing.T) {
	g := NewPlanGenerator()
	rows := []PlanRow{
		{Ordinal: 1, StepType: StepTypeFixture, ScriptPath: ptr("./t/__test__/_setup.sql"), Directory: "./t/__test__/", Depth: 0},
		{Ordinal: 2, StepType: StepTypeTest, ScriptPath: ptr("./t/__test__/01_test.sql"), Directory: "./t/__test__/", Depth: 0},
		{Ordinal: 3, StepType: StepTypeTeardown, ScriptPath: nil, Directory: "./t/__test__/", Depth: 0},
	}

	result := g.Generate(rows, "")

	// Should contain fixture path
	if !strings.Contains(result.SQL, "_setup.sql") {
		t.Errorf("SQL should contain fixture path, got: %s", result.SQL)
	}

	// Should contain test path
	if !strings.Contains(result.SQL, "01_test.sql") {
		t.Errorf("SQL should contain test path, got: %s", result.SQL)
	}

	// Should contain RELEASE SAVEPOINT for teardown
	if !strings.Contains(result.SQL, "RELEASE SAVEPOINT") {
		t.Errorf("SQL should contain RELEASE SAVEPOINT for teardown, got: %s", result.SQL)
	}
}

func TestPlanGenerator_Generate_WithCallback(t *testing.T) {
	g := NewPlanGenerator()
	rows := []PlanRow{
		{Ordinal: 1, StepType: StepTypeFixture, ScriptPath: ptr("./t/__test__/_setup.sql"), Directory: "./t/__test__/", Depth: 0},
		{Ordinal: 2, StepType: StepTypeTest, ScriptPath: ptr("./t/__test__/test.sql"), Directory: "./t/__test__/", Depth: 0},
		{Ordinal: 3, StepType: StepTypeTeardown, ScriptPath: nil, Directory: "./t/__test__/", Depth: 0},
	}

	result := g.Generate(rows, "pg_temp.observer")

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

	// Check for teardown callbacks
	if !strings.Contains(result.SQL, "ROW('teardown_start'") {
		t.Errorf("Should contain teardown_start callback, got: %s", result.SQL)
	}
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

func TestPlanGenerator_Generate_NoCallbackNoEvents(t *testing.T) {
	g := NewPlanGenerator()
	rows := []PlanRow{
		{Ordinal: 1, StepType: StepTypeTest, ScriptPath: ptr("./t/__test__/test.sql"), Directory: "./t/__test__/", Depth: 0},
	}

	result := g.Generate(rows, "")

	// Empty callback = no callback invocations
	if strings.Contains(result.SQL, "pgmi_test_event") {
		t.Error("Empty callback should not produce callback invocations")
	}

	// Should still have EXECUTE statement
	if !strings.Contains(result.SQL, "EXECUTE") {
		t.Error("SQL should contain EXECUTE statement")
	}
}

func TestPlanGenerator_Generate_SourceMap(t *testing.T) {
	g := NewPlanGenerator()
	rows := []PlanRow{
		{Ordinal: 1, StepType: StepTypeTest, ScriptPath: ptr("./test/__test__/01_test.sql"), Directory: "./test/__test__/", Depth: 0},
	}

	result := g.Generate(rows, "")

	if result.SourceMap == nil {
		t.Fatal("SourceMap should not be nil")
	}
	if result.SourceMap.Len() == 0 {
		t.Error("SourceMap should have entries")
	}
}

func TestPlanGenerator_Generate_MultipleDirectories(t *testing.T) {
	g := NewPlanGenerator()
	rows := []PlanRow{
		{Ordinal: 1, StepType: StepTypeFixture, ScriptPath: ptr("./a/__test__/_setup.sql"), Directory: "./a/__test__/", Depth: 0},
		{Ordinal: 2, StepType: StepTypeTest, ScriptPath: ptr("./a/__test__/test.sql"), Directory: "./a/__test__/", Depth: 0},
		{Ordinal: 3, StepType: StepTypeTeardown, ScriptPath: nil, Directory: "./a/__test__/", Depth: 0},
		{Ordinal: 4, StepType: StepTypeFixture, ScriptPath: ptr("./b/__test__/_setup.sql"), Directory: "./b/__test__/", Depth: 0},
		{Ordinal: 5, StepType: StepTypeTest, ScriptPath: ptr("./b/__test__/test.sql"), Directory: "./b/__test__/", Depth: 0},
		{Ordinal: 6, StepType: StepTypeTeardown, ScriptPath: nil, Directory: "./b/__test__/", Depth: 0},
	}

	result := g.Generate(rows, "")

	// Should contain both directories
	if !strings.Contains(result.SQL, "./a/__test__/") {
		t.Error("SQL should contain ./a/__test__/")
	}
	if !strings.Contains(result.SQL, "./b/__test__/") {
		t.Error("SQL should contain ./b/__test__/")
	}

	// Should have multiple savepoints with different names
	if strings.Count(result.SQL, "SAVEPOINT __pgmi_d") < 2 {
		t.Error("Should have at least 2 directory savepoints")
	}
}

func TestPlanGenerator_Generate_TestWithoutFixture(t *testing.T) {
	g := NewPlanGenerator()
	rows := []PlanRow{
		{Ordinal: 1, StepType: StepTypeTest, ScriptPath: ptr("./t/__test__/test.sql"), Directory: "./t/__test__/", Depth: 0},
		{Ordinal: 2, StepType: StepTypeTeardown, ScriptPath: nil, Directory: "./t/__test__/", Depth: 0},
	}

	result := g.Generate(rows, "")

	// Should still work without fixture
	if !strings.Contains(result.SQL, "SAVEPOINT") {
		t.Error("Should create savepoint even without fixture")
	}
	if !strings.Contains(result.SQL, "EXECUTE") {
		t.Error("Should contain EXECUTE for test")
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "'simple'"},
		{"with'quote", "'with''quote'"},
		{"multiple''quotes", "'multiple''''quotes'"},
		{"./path/to/file.sql", "'./path/to/file.sql'"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := quoteLiteral(tt.input)
			if result != tt.expected {
				t.Errorf("quoteLiteral(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}
