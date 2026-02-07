package testgen

import (
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testdiscovery"
)

func ptr(s string) *string {
	return &s
}

func TestGenerator_EmptySteps(t *testing.T) {
	gen := New(DefaultConfig())
	result := gen.Generate(nil, "./project", ".*")

	if result.FixtureCount != 0 {
		t.Errorf("expected 0 fixtures, got %d", result.FixtureCount)
	}
	if result.TestCount != 0 {
		t.Errorf("expected 0 tests, got %d", result.TestCount)
	}
	if result.TeardownCount != 0 {
		t.Errorf("expected 0 teardowns, got %d", result.TeardownCount)
	}

	if !strings.Contains(result.Script, "BEGIN;") {
		t.Error("expected BEGIN in script")
	}
	if !strings.Contains(result.Script, "ROLLBACK;") {
		t.Error("expected ROLLBACK in script")
	}
}

func TestGenerator_SingleTest(t *testing.T) {
	gen := New(DefaultConfig())
	steps := []testdiscovery.TestScriptRow{
		{
			Ordinal:    1,
			StepType:   testdiscovery.StepTypeFixture,
			ScriptPath: ptr("./__test__/_setup.sql"),
			Directory:  "./__test__/",
			Depth:      0,
			PreExec:    ptr("SAVEPOINT __pgmi_0__;"),
			ScriptSQL:  ptr("CREATE TEMP TABLE test_data (id int);"),
		},
		{
			Ordinal:    2,
			StepType:   testdiscovery.StepTypeTest,
			ScriptPath: ptr("./__test__/test_example.sql"),
			Directory:  "./__test__/",
			Depth:      0,
			PreExec:    ptr("SAVEPOINT __pgmi_1__;"),
			ScriptSQL:  ptr("SELECT 1;"),
			PostExec:   ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"),
		},
		{
			Ordinal:   3,
			StepType:  testdiscovery.StepTypeTeardown,
			Directory: "./__test__/",
			Depth:     0,
			PreExec:   ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
			PostExec:  ptr("RELEASE SAVEPOINT __pgmi_0__;"),
		},
	}

	result := gen.Generate(steps, "./project", ".*")

	if result.FixtureCount != 1 {
		t.Errorf("expected 1 fixture, got %d", result.FixtureCount)
	}
	if result.TestCount != 1 {
		t.Errorf("expected 1 test, got %d", result.TestCount)
	}
	if result.TeardownCount != 1 {
		t.Errorf("expected 1 teardown, got %d", result.TeardownCount)
	}

	if !strings.Contains(result.Script, "Fixture: ./__test__/_setup.sql") {
		t.Error("expected fixture comment in script")
	}
	if !strings.Contains(result.Script, "Test: ./__test__/test_example.sql") {
		t.Error("expected test comment in script")
	}
	if !strings.Contains(result.Script, "Teardown: ./__test__/") {
		t.Error("expected teardown comment in script")
	}
	if !strings.Contains(result.Script, "RAISE NOTICE") {
		t.Error("expected RAISE NOTICE in script")
	}
	if !strings.Contains(result.Script, "SAVEPOINT __pgmi_0__") {
		t.Error("expected savepoint in script")
	}
}

func TestGenerator_WithoutTransaction(t *testing.T) {
	config := Config{
		WithTransaction: false,
		WithNotices:     true,
		WithDebug:       false,
	}
	gen := New(config)
	result := gen.Generate(nil, "./project", ".*")

	if strings.Contains(result.Script, "BEGIN;") {
		t.Error("did not expect BEGIN in script")
	}
	if strings.Contains(result.Script, "ROLLBACK;") {
		t.Error("did not expect ROLLBACK in script")
	}
}

func TestGenerator_WithoutNotices(t *testing.T) {
	config := Config{
		WithTransaction: true,
		WithNotices:     false,
		WithDebug:       false,
	}
	gen := New(config)
	steps := []testdiscovery.TestScriptRow{
		{
			Ordinal:    1,
			StepType:   testdiscovery.StepTypeTest,
			ScriptPath: ptr("./__test__/test_example.sql"),
			Directory:  "./__test__/",
			ScriptSQL:  ptr("SELECT 1;"),
		},
	}

	result := gen.Generate(steps, "./project", ".*")

	if strings.Contains(result.Script, "RAISE NOTICE") {
		t.Error("did not expect RAISE NOTICE in script")
	}
}

func TestGenerator_WithDebug(t *testing.T) {
	config := Config{
		WithTransaction: true,
		WithNotices:     true,
		WithDebug:       true,
	}
	gen := New(config)
	steps := []testdiscovery.TestScriptRow{
		{
			Ordinal:    1,
			StepType:   testdiscovery.StepTypeTest,
			ScriptPath: ptr("./__test__/test_example.sql"),
			Directory:  "./__test__/",
			PreExec:    ptr("SAVEPOINT __pgmi_0__;"),
			ScriptSQL:  ptr("SELECT 1;"),
			PostExec:   ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
		},
	}

	result := gen.Generate(steps, "./project", ".*")

	if !strings.Contains(result.Script, "RAISE DEBUG") {
		t.Error("expected RAISE DEBUG in script")
	}
}

func TestGenerator_QuotesSpecialCharacters(t *testing.T) {
	gen := New(DefaultConfig())
	steps := []testdiscovery.TestScriptRow{
		{
			Ordinal:    1,
			StepType:   testdiscovery.StepTypeTest,
			ScriptPath: ptr("./__test__/test_with'quote.sql"),
			Directory:  "./__test__/",
			ScriptSQL:  ptr("SELECT 1;"),
		},
	}

	result := gen.Generate(steps, "./project", ".*")

	if !strings.Contains(result.Script, "test_with''quote.sql") {
		t.Error("expected escaped quote in script")
	}
}

func TestGenerator_Header(t *testing.T) {
	gen := New(DefaultConfig())
	result := gen.Generate(nil, "./myproject", "auth.*")

	if !strings.Contains(result.Script, "Source: ./myproject") {
		t.Error("expected source path in header")
	}
	if !strings.Contains(result.Script, "Filter: auth.*") {
		t.Error("expected filter in header")
	}
	if !strings.Contains(result.Script, "Generated:") {
		t.Error("expected generation timestamp in header")
	}
}

func TestGenerator_WithCallback_IncludesInfrastructure(t *testing.T) {
	config := Config{
		WithTransaction: true,
		WithNotices:     true,
		WithDebug:       false,
		Callback:        "pg_temp.test_observer",
	}
	gen := New(config)
	result := gen.Generate(nil, "./project", ".*")

	// Should include callback in header
	if !strings.Contains(result.Script, "Callback: pg_temp.test_observer") {
		t.Error("expected callback in header")
	}

	// Should include type definition
	if !strings.Contains(result.Script, "CREATE TYPE pg_temp.pgmi_test_event AS") {
		t.Error("expected pgmi_test_event type definition")
	}

	// Should include callback stub
	if !strings.Contains(result.Script, "CREATE OR REPLACE FUNCTION pg_temp.test_observer") {
		t.Error("expected callback function definition")
	}

	// Should include suite callbacks even with no steps
	if !strings.Contains(result.Script, "suite_start") {
		t.Error("expected suite_start callback")
	}
	if !strings.Contains(result.Script, "suite_end") {
		t.Error("expected suite_end callback")
	}
}

func TestGenerator_WithCallback_EmitsAllEvents(t *testing.T) {
	config := Config{
		WithTransaction: true,
		WithNotices:     true,
		WithDebug:       false,
		Callback:        "pg_temp.observer",
	}
	gen := New(config)
	steps := []testdiscovery.TestScriptRow{
		{
			Ordinal:    1,
			StepType:   testdiscovery.StepTypeFixture,
			ScriptPath: ptr("./__test__/_setup.sql"),
			Directory:  "./__test__/",
			Depth:      0,
			PreExec:    ptr("SAVEPOINT __pgmi_0__;"),
			ScriptSQL:  ptr("CREATE TEMP TABLE test_data (id int);"),
		},
		{
			Ordinal:    2,
			StepType:   testdiscovery.StepTypeTest,
			ScriptPath: ptr("./__test__/test_example.sql"),
			Directory:  "./__test__/",
			Depth:      0,
			PreExec:    ptr("SAVEPOINT __pgmi_1__;"),
			ScriptSQL:  ptr("SELECT 1;"),
			PostExec:   ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"),
		},
		{
			Ordinal:   3,
			StepType:  testdiscovery.StepTypeTeardown,
			Directory: "./__test__/",
			Depth:     0,
			PreExec:   ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
			PostExec:  ptr("RELEASE SAVEPOINT __pgmi_0__;"),
		},
	}

	result := gen.Generate(steps, "./project", ".*")

	// All event types should be present
	expectedEvents := []string{
		"suite_start", "suite_end",
		"fixture_start", "fixture_end",
		"test_start", "test_end",
		"rollback",
		"teardown_start", "teardown_end",
	}

	for _, event := range expectedEvents {
		if !strings.Contains(result.Script, "'"+event+"'") {
			t.Errorf("expected %s callback event", event)
		}
	}

	// Should use the callback function
	if strings.Count(result.Script, "pg_temp.observer(ROW(") < 9 {
		t.Error("expected at least 9 callback invocations")
	}

	// Should NOT have RAISE NOTICE (callback replaces notices)
	// Fixture/test start callbacks replace the DO $$ RAISE NOTICE blocks
	if strings.Contains(result.Script, "DO $$ BEGIN RAISE NOTICE 'Fixture:") {
		t.Error("callback should replace RAISE NOTICE for fixtures")
	}
	if strings.Contains(result.Script, "DO $$ BEGIN RAISE NOTICE 'Test:") {
		t.Error("callback should replace RAISE NOTICE for tests")
	}
}

func TestGenerator_WithCallback_TypeCast(t *testing.T) {
	config := Config{
		WithTransaction: true,
		Callback:        "pg_temp.cb",
	}
	gen := New(config)
	steps := []testdiscovery.TestScriptRow{
		{
			Ordinal:    1,
			StepType:   testdiscovery.StepTypeTest,
			ScriptPath: ptr("./__test__/test.sql"),
			Directory:  "./__test__/",
			ScriptSQL:  ptr("SELECT 1;"),
		},
	}

	result := gen.Generate(steps, "./project", ".*")

	// Should properly cast to the event type
	if !strings.Contains(result.Script, "::pg_temp.pgmi_test_event") {
		t.Error("expected type cast in callback invocations")
	}
}

func TestGenerator_WithoutCallback_NoCallbackContent(t *testing.T) {
	config := DefaultConfig() // No callback
	gen := New(config)
	steps := []testdiscovery.TestScriptRow{
		{
			Ordinal:    1,
			StepType:   testdiscovery.StepTypeTest,
			ScriptPath: ptr("./__test__/test.sql"),
			Directory:  "./__test__/",
			ScriptSQL:  ptr("SELECT 1;"),
		},
	}

	result := gen.Generate(steps, "./project", ".*")

	// Should NOT include callback infrastructure
	if strings.Contains(result.Script, "pgmi_test_event") {
		t.Error("should not include pgmi_test_event without callback")
	}
	if strings.Contains(result.Script, "suite_start") {
		t.Error("should not include suite_start without callback")
	}
}
