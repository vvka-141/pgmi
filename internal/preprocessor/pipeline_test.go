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
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 'test';"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
		{Ordinal: 2, StepType: "teardown", PreExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
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

	// Should contain embedded SQL content
	if !strings.Contains(result.ExpandedSQL, "SELECT 'test'") {
		t.Errorf("ExpandedSQL should contain embedded content, got: %s", result.ExpandedSQL)
	}
}

func TestPipeline_Process_PlanMode_SingleMacro(t *testing.T) {
	p := NewPipeline(true) // plan mode

	sql := `PERFORM pgmi_plan_test();`
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 'test';"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), Directory: "./test/__test__"},
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
		{Ordinal: 1, StepType: "fixture", ScriptPath: testdiscovery.Ptr("./a/__test__/_setup.sql"), ScriptSQL: testdiscovery.Ptr("-- fixture a"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./a/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: testdiscovery.Ptr("./a/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("-- test a"), Directory: "./a/__test__"},
		{Ordinal: 3, StepType: "teardown", PreExec: testdiscovery.Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: testdiscovery.Ptr("RELEASE SAVEPOINT __pgmi_0__;"), Directory: "./a/__test__"},
		{Ordinal: 4, StepType: "fixture", ScriptPath: testdiscovery.Ptr("./b/__test__/_setup.sql"), ScriptSQL: testdiscovery.Ptr("-- fixture b"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), Directory: "./b/__test__"},
		{Ordinal: 5, StepType: "test", ScriptPath: testdiscovery.Ptr("./b/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("-- test b"), Directory: "./b/__test__"},
		{Ordinal: 6, StepType: "teardown", Directory: "./b/__test__"},
	}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	// Should only include ./a/__test__ tests
	if strings.Contains(result.ExpandedSQL, "-- test b") {
		t.Errorf("ExpandedSQL should not contain test b (filtered out), got: %s", result.ExpandedSQL)
	}
	if !strings.Contains(result.ExpandedSQL, "-- test a") {
		t.Errorf("ExpandedSQL should contain test a, got: %s", result.ExpandedSQL)
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
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./test/__test__/01_test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), Directory: "./test/__test__"},
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
		{Ordinal: 1, StepType: "fixture", ScriptPath: testdiscovery.Ptr("./a/__test__/_setup.sql"), ScriptSQL: testdiscovery.Ptr("-- fixture a"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./a/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: testdiscovery.Ptr("./a/__test__/01.sql"), ScriptSQL: testdiscovery.Ptr("-- test a"), Directory: "./a/__test__"},
		{Ordinal: 3, StepType: "teardown", Directory: "./a/__test__"},
		{Ordinal: 4, StepType: "fixture", ScriptPath: testdiscovery.Ptr("./b/__test__/_setup.sql"), ScriptSQL: testdiscovery.Ptr("-- fixture b"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_1__;"), Directory: "./b/__test__"},
		{Ordinal: 5, StepType: "test", ScriptPath: testdiscovery.Ptr("./b/__test__/01.sql"), ScriptSQL: testdiscovery.Ptr("-- test b"), Directory: "./b/__test__"},
		{Ordinal: 6, StepType: "teardown", Directory: "./b/__test__"},
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

// TestPipeline_ProcessWithTree tests the tree-based interface

func TestPipeline_ProcessWithTree_NoMacros(t *testing.T) {
	p := NewPipeline(false)

	sql := `SELECT 1; SELECT 2;`
	tree := testdiscovery.NewTestTree()
	resolver := func(path string) (string, error) { return "", nil }

	result, err := p.ProcessWithTree(sql, tree, resolver)
	if err != nil {
		t.Fatalf("ProcessWithTree() error = %v", err)
	}

	if result.ExpandedSQL != sql {
		t.Errorf("Expected original SQL, got %q", result.ExpandedSQL)
	}
	if result.MacroCount != 0 {
		t.Errorf("MacroCount = %d, expected 0", result.MacroCount)
	}
}

func TestPipeline_ProcessWithTree_FilteredSavepointsAreSequential(t *testing.T) {
	p := NewPipeline(false)

	// Create tree with two directories
	tree := testdiscovery.NewTestTree()

	dirA := testdiscovery.NewTestDirectory("./a/__test__", 0)
	dirA.SetFixture(&testdiscovery.TestFile{Path: "./a/__test__/_setup.sql", IsFixture: true})
	dirA.AddTest(&testdiscovery.TestFile{Path: "./a/__test__/01_test.sql"})
	tree.AddDirectory(dirA)

	dirB := testdiscovery.NewTestDirectory("./b/__test__", 0)
	dirB.SetFixture(&testdiscovery.TestFile{Path: "./b/__test__/_setup.sql", IsFixture: true})
	dirB.AddTest(&testdiscovery.TestFile{Path: "./b/__test__/01_test.sql"})
	tree.AddDirectory(dirB)

	resolver := func(path string) (string, error) {
		return "-- content of " + path, nil
	}

	// Filter to only ./b/** - savepoints should start at 0, not 1
	sql := `SELECT pgmi_test('./b/**');`

	result, err := p.ProcessWithTree(sql, tree, resolver)
	if err != nil {
		t.Fatalf("ProcessWithTree() error = %v", err)
	}

	// Should use __pgmi_0__ (sequential from start), not __pgmi_1__
	if !strings.Contains(result.ExpandedSQL, "__pgmi_0__") {
		t.Errorf("Filtered result should use __pgmi_0__, got: %s", result.ExpandedSQL)
	}

	// Should NOT contain ./a content
	if strings.Contains(result.ExpandedSQL, "./a/__test__") {
		t.Errorf("Should not contain ./a content, got: %s", result.ExpandedSQL)
	}

	// Should contain ./b content
	if !strings.Contains(result.ExpandedSQL, "./b/__test__") {
		t.Errorf("Should contain ./b content, got: %s", result.ExpandedSQL)
	}
}

func TestPipeline_ProcessWithTree_PreservesAncestorFixtures(t *testing.T) {
	p := NewPipeline(false)

	// Create nested structure
	tree := testdiscovery.NewTestTree()

	parent := testdiscovery.NewTestDirectory("./a/__test__", 0)
	parent.SetFixture(&testdiscovery.TestFile{Path: "./a/__test__/_setup.sql", IsFixture: true})

	child := testdiscovery.NewTestDirectory("./a/__test__/nested", 1)
	child.SetFixture(&testdiscovery.TestFile{Path: "./a/__test__/nested/_setup.sql", IsFixture: true})
	child.AddTest(&testdiscovery.TestFile{Path: "./a/__test__/nested/01_test.sql"})

	parent.AddChild(child)
	tree.AddDirectory(parent)

	resolver := func(path string) (string, error) {
		return "-- content of " + path, nil
	}

	// Filter for nested only - should include parent fixture
	sql := `SELECT pgmi_test('./a/__test__/nested/**');`

	result, err := p.ProcessWithTree(sql, tree, resolver)
	if err != nil {
		t.Fatalf("ProcessWithTree() error = %v", err)
	}

	// Should contain both fixtures
	if !strings.Contains(result.ExpandedSQL, "./a/__test__/_setup.sql") {
		t.Errorf("Should contain parent fixture, got: %s", result.ExpandedSQL)
	}
	if !strings.Contains(result.ExpandedSQL, "./a/__test__/nested/_setup.sql") {
		t.Errorf("Should contain nested fixture, got: %s", result.ExpandedSQL)
	}
}

func TestPipeline_ProcessWithTree_EmptyPatternUsesAllTests(t *testing.T) {
	p := NewPipeline(false)

	tree := testdiscovery.NewTestTree()

	dir := testdiscovery.NewTestDirectory("./a/__test__", 0)
	dir.AddTest(&testdiscovery.TestFile{Path: "./a/__test__/01_test.sql"})
	dir.AddTest(&testdiscovery.TestFile{Path: "./a/__test__/02_test.sql"})
	tree.AddDirectory(dir)

	resolver := func(path string) (string, error) {
		return "-- content of " + path, nil
	}

	sql := `SELECT pgmi_test();`

	result, err := p.ProcessWithTree(sql, tree, resolver)
	if err != nil {
		t.Fatalf("ProcessWithTree() error = %v", err)
	}

	// Should contain both tests
	if !strings.Contains(result.ExpandedSQL, "01_test.sql") {
		t.Errorf("Should contain 01_test.sql")
	}
	if !strings.Contains(result.ExpandedSQL, "02_test.sql") {
		t.Errorf("Should contain 02_test.sql")
	}
}

func TestPipeline_ProcessWithTree_MultipleMacrosDifferentPatterns(t *testing.T) {
	p := NewPipeline(false)

	tree := testdiscovery.NewTestTree()

	dirA := testdiscovery.NewTestDirectory("./a/__test__", 0)
	dirA.AddTest(&testdiscovery.TestFile{Path: "./a/__test__/01_test.sql"})
	tree.AddDirectory(dirA)

	dirB := testdiscovery.NewTestDirectory("./b/__test__", 0)
	dirB.AddTest(&testdiscovery.TestFile{Path: "./b/__test__/01_test.sql"})
	tree.AddDirectory(dirB)

	resolver := func(path string) (string, error) {
		return "-- test content: " + path, nil
	}

	sql := `
		SELECT pgmi_test('./a/**');
		SELECT pgmi_test('./b/**');
	`

	result, err := p.ProcessWithTree(sql, tree, resolver)
	if err != nil {
		t.Fatalf("ProcessWithTree() error = %v", err)
	}

	if result.MacroCount != 2 {
		t.Errorf("MacroCount = %d, expected 2", result.MacroCount)
	}

	// Both should be in output
	if !strings.Contains(result.ExpandedSQL, "./a/__test__/01_test.sql") {
		t.Errorf("Should contain ./a test")
	}
	if !strings.Contains(result.ExpandedSQL, "./b/__test__/01_test.sql") {
		t.Errorf("Should contain ./b test")
	}
}

func TestPipeline_ProcessWithTree_CallbackPropagation(t *testing.T) {
	p := NewPipeline(false)

	sql := `SELECT pgmi_test('./t/**', 'pg_temp.my_cb');`

	tree := testdiscovery.NewTestTree()
	dir := testdiscovery.NewTestDirectory("./t/__test__", 0)
	dir.AddTest(&testdiscovery.TestFile{Path: "./t/__test__/test.sql"})
	tree.AddDirectory(dir)

	resolver := func(path string) (string, error) { return "SELECT 1;", nil }

	result, err := p.ProcessWithTree(sql, tree, resolver)
	if err != nil {
		t.Fatalf("ProcessWithTree() error = %v", err)
	}

	// Should contain callback function name
	if !strings.Contains(result.ExpandedSQL, "pg_temp.my_cb") {
		t.Errorf("Should contain callback function name, got: %s", result.ExpandedSQL)
	}

	// Should contain suite_start event
	if !strings.Contains(result.ExpandedSQL, "suite_start") {
		t.Errorf("Should contain suite_start event, got: %s", result.ExpandedSQL)
	}

	// Should contain pgmi_test_event type cast
	if !strings.Contains(result.ExpandedSQL, "::pg_temp.pgmi_test_event") {
		t.Errorf("Should contain pgmi_test_event type cast, got: %s", result.ExpandedSQL)
	}
}

func TestPipeline_ProcessWithTree_NoCallbackNoEvents(t *testing.T) {
	p := NewPipeline(false)

	// No callback in macro call
	sql := `SELECT pgmi_test('./t/**');`

	tree := testdiscovery.NewTestTree()
	dir := testdiscovery.NewTestDirectory("./t/__test__", 0)
	dir.AddTest(&testdiscovery.TestFile{Path: "./t/__test__/test.sql"})
	tree.AddDirectory(dir)

	resolver := func(path string) (string, error) { return "SELECT 1;", nil }

	result, err := p.ProcessWithTree(sql, tree, resolver)
	if err != nil {
		t.Fatalf("ProcessWithTree() error = %v", err)
	}

	// Should NOT contain callback-related content
	if strings.Contains(result.ExpandedSQL, "pgmi_test_event") {
		t.Errorf("Should not contain pgmi_test_event when no callback, got: %s", result.ExpandedSQL)
	}
}

func TestPipeline_Process_CallbackPropagation(t *testing.T) {
	p := NewPipeline(false)

	sql := `SELECT pgmi_test('./t/**', 'pg_temp.observer');`
	rows := []testdiscovery.TestScriptRow{
		{Ordinal: 1, StepType: "test", ScriptPath: testdiscovery.Ptr("./t/__test__/test.sql"), ScriptSQL: testdiscovery.Ptr("SELECT 1;"), PreExec: testdiscovery.Ptr("SAVEPOINT __pgmi_0__;"), Directory: "./t/__test__"},
	}

	result, err := p.Process(sql, rows)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	// Should contain callback invocations
	if !strings.Contains(result.ExpandedSQL, "pg_temp.observer") {
		t.Errorf("Should contain callback function name, got: %s", result.ExpandedSQL)
	}
	if !strings.Contains(result.ExpandedSQL, "suite_start") {
		t.Errorf("Should contain suite_start event, got: %s", result.ExpandedSQL)
	}
}
