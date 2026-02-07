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
			StepType:   "fixture",
			ScriptPath: ptr("./__test__/_setup.sql"),
			Directory:  "./__test__/",
			Depth:      0,
			PreExec:    ptr("SAVEPOINT __pgmi_0__;"),
			ScriptSQL:  ptr("CREATE TEMP TABLE test_data (id int);"),
		},
		{
			Ordinal:    2,
			StepType:   "test",
			ScriptPath: ptr("./__test__/test_example.sql"),
			Directory:  "./__test__/",
			Depth:      0,
			PreExec:    ptr("SAVEPOINT __pgmi_1__;"),
			ScriptSQL:  ptr("SELECT 1;"),
			PostExec:   ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"),
		},
		{
			Ordinal:   3,
			StepType:  "teardown",
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
			StepType:   "test",
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
			StepType:   "test",
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
			StepType:   "test",
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
