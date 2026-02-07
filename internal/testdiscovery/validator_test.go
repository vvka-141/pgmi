package testdiscovery

import (
	"testing"
)

func TestNewSavepointValidator(t *testing.T) {
	v := NewSavepointValidator()
	if v == nil {
		t.Fatal("NewSavepointValidator() returned nil")
	}
	if v.savepoints == nil {
		t.Error("savepoints map should be initialized")
	}
	if v.stack == nil {
		t.Error("stack should be initialized")
	}
}

func TestValidator_EmptyPlan(t *testing.T) {
	v := NewSavepointValidator()
	result := v.Validate(nil)
	if !result.Valid {
		t.Errorf("empty plan should be valid, got errors: %v", result.Errors)
	}

	result = v.Validate([]TestScriptRow{})
	if !result.Valid {
		t.Errorf("empty slice should be valid, got errors: %v", result.Errors)
	}
}

func TestValidator_ValidSingleTest(t *testing.T) {
	rows := []TestScriptRow{
		{
			Ordinal:   1,
			StepType:  "test",
			Directory: "./test/__test__",
			PreExec:   Ptr("SAVEPOINT __pgmi_0__;"),
			PostExec:  Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
		},
		{
			Ordinal:   2,
			StepType:  "teardown",
			Directory: "./test/__test__",
			PreExec:   Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
			PostExec:  Ptr("RELEASE SAVEPOINT __pgmi_0__;"),
		},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if !result.Valid {
		t.Errorf("valid single test should pass, got errors: %v", result.Errors)
	}
}

func TestValidator_ValidFixtureWithTests(t *testing.T) {
	rows := []TestScriptRow{
		{
			Ordinal:   1,
			StepType:  "fixture",
			Directory: "./test/__test__",
			PreExec:   Ptr("SAVEPOINT __pgmi_0__;"),
		},
		{
			Ordinal:   2,
			StepType:  "test",
			Directory: "./test/__test__",
			PreExec:   Ptr("SAVEPOINT __pgmi_1__;"),
			PostExec:  Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"),
		},
		{
			Ordinal:   3,
			StepType:  "test",
			Directory: "./test/__test__",
			PostExec:  Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"),
		},
		{
			Ordinal:   4,
			StepType:  "teardown",
			Directory: "./test/__test__",
			PreExec:   Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"),
			PostExec:  Ptr("RELEASE SAVEPOINT __pgmi_0__;"),
		},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if !result.Valid {
		t.Errorf("valid fixture with tests should pass, got errors: %v", result.Errors)
	}
}

func TestValidator_ValidNestedDirectories(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "fixture", Directory: "./parent/__test__", PreExec: Ptr("SAVEPOINT __pgmi_0__;")},
		{Ordinal: 2, StepType: "test", Directory: "./parent/__test__", PreExec: Ptr("SAVEPOINT __pgmi_1__;"), PostExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;")},
		{Ordinal: 3, StepType: "fixture", Directory: "./parent/__test__/child", PreExec: Ptr("SAVEPOINT __pgmi_2__;")},
		{Ordinal: 4, StepType: "test", Directory: "./parent/__test__/child", PreExec: Ptr("SAVEPOINT __pgmi_3__;"), PostExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_3__;")},
		{Ordinal: 5, StepType: "teardown", Directory: "./parent/__test__/child", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_2__;"), PostExec: Ptr("RELEASE SAVEPOINT __pgmi_2__;")},
		{Ordinal: 6, StepType: "teardown", Directory: "./parent/__test__", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: Ptr("RELEASE SAVEPOINT __pgmi_0__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if !result.Valid {
		t.Errorf("valid nested directories should pass, got errors: %v", result.Errors)
	}
}

func TestValidator_FailNonMonotonicOrdinal(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "test", Directory: "./test/__test__", PreExec: Ptr("SAVEPOINT __pgmi_0__;"), PostExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;")},
		{Ordinal: 1, StepType: "teardown", Directory: "./test/__test__", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: Ptr("RELEASE SAVEPOINT __pgmi_0__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if result.Valid {
		t.Error("non-monotonic ordinal should fail")
	}

	found := false
	for _, err := range result.Errors {
		if err.Invariant == "MonotonicOrdinals" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected MonotonicOrdinals error")
	}
}

func TestValidator_FailMissingRollback(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "test", Directory: "./test/__test__", PreExec: Ptr("SAVEPOINT __pgmi_0__;")},
		{Ordinal: 2, StepType: "teardown", Directory: "./test/__test__", PostExec: Ptr("RELEASE SAVEPOINT __pgmi_0__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if result.Valid {
		t.Error("missing rollback should fail")
	}

	found := false
	for _, err := range result.Errors {
		if err.Invariant == "SavepointPairing" && err.Message == "savepoint missing ROLLBACK TO" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected SavepointPairing error for missing ROLLBACK, got: %v", result.Errors)
	}
}

func TestValidator_FailMissingRelease(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "test", Directory: "./test/__test__", PreExec: Ptr("SAVEPOINT __pgmi_0__;"), PostExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;")},
		{Ordinal: 2, StepType: "teardown", Directory: "./test/__test__", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if result.Valid {
		t.Error("missing release should fail")
	}

	found := false
	for _, err := range result.Errors {
		if err.Invariant == "SavepointPairing" && err.Message == "savepoint missing RELEASE" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected SavepointPairing error for missing RELEASE, got: %v", result.Errors)
	}
}

func TestValidator_FailCrossRelease(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "fixture", Directory: "./parent/__test__", PreExec: Ptr("SAVEPOINT __pgmi_0__;")},
		{Ordinal: 2, StepType: "fixture", Directory: "./parent/__test__/child", PreExec: Ptr("SAVEPOINT __pgmi_1__;")},
		{Ordinal: 3, StepType: "teardown", Directory: "./parent/__test__", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: Ptr("RELEASE SAVEPOINT __pgmi_0__;")},
		{Ordinal: 4, StepType: "teardown", Directory: "./parent/__test__/child", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_1__;"), PostExec: Ptr("RELEASE SAVEPOINT __pgmi_1__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if result.Valid {
		t.Error("cross-release (parent before child) should fail")
	}

	found := false
	for _, err := range result.Errors {
		if err.Invariant == "NestingOrder" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected NestingOrder error, got: %v", result.Errors)
	}
}

func TestValidator_FailMissingTeardown(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "test", Directory: "./test/__test__", PreExec: Ptr("SAVEPOINT __pgmi_0__;"), PostExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if result.Valid {
		t.Error("missing teardown should fail")
	}

	found := false
	for _, err := range result.Errors {
		if err.Invariant == "DirectoryStructure" && err.Message == "each directory must have exactly one teardown" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected DirectoryStructure error for missing teardown, got: %v", result.Errors)
	}
}

func TestValidator_FailFixtureAfterTest(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "test", Directory: "./test/__test__", PreExec: Ptr("SAVEPOINT __pgmi_0__;"), PostExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;")},
		{Ordinal: 2, StepType: "fixture", Directory: "./test/__test__", PreExec: Ptr("SAVEPOINT __pgmi_1__;")},
		{Ordinal: 3, StepType: "teardown", Directory: "./test/__test__", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: Ptr("RELEASE SAVEPOINT __pgmi_0__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if result.Valid {
		t.Error("fixture after test should fail")
	}

	found := false
	for _, err := range result.Errors {
		if err.Invariant == "DirectoryStructure" && err.Message == "fixture must come before tests in same directory" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected DirectoryStructure error for fixture after test, got: %v", result.Errors)
	}
}

func TestValidator_FailOrphanSavepoint(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "test", Directory: "./test/__test__", PreExec: Ptr("SAVEPOINT __pgmi_0__;"), PostExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;")},
		{Ordinal: 2, StepType: "teardown", Directory: "./test/__test__", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if result.Valid {
		t.Error("orphan savepoint (no release) should fail")
	}

	foundPairing := false
	foundOrphan := false
	for _, err := range result.Errors {
		if err.Invariant == "SavepointPairing" {
			foundPairing = true
		}
		if err.Invariant == "NoOrphans" {
			foundOrphan = true
		}
	}
	if !foundPairing && !foundOrphan {
		t.Errorf("expected pairing or orphan error, got: %v", result.Errors)
	}
}

func TestParseSavepoint(t *testing.T) {
	tests := []struct {
		sql      string
		wantName string
		wantOp   SavepointOperation
		wantOk   bool
	}{
		{"SAVEPOINT __pgmi_0__;", "__pgmi_0__", OpSavepoint, true},
		{"SAVEPOINT __pgmi_123__;", "__pgmi_123__", OpSavepoint, true},
		{"SAVEPOINT __pgmi_0__", "__pgmi_0__", OpSavepoint, true},
		{"  SAVEPOINT __pgmi_0__;  ", "__pgmi_0__", OpSavepoint, true},
		{"ROLLBACK TO SAVEPOINT __pgmi_0__;", "__pgmi_0__", OpRollback, true},
		{"ROLLBACK TO SAVEPOINT __pgmi_42__;", "__pgmi_42__", OpRollback, true},
		{"RELEASE SAVEPOINT __pgmi_0__;", "__pgmi_0__", OpRelease, true},
		{"RELEASE SAVEPOINT __pgmi_99__;", "__pgmi_99__", OpRelease, true},
		{"SELECT 1", "", "", false},
		{"BEGIN;", "", "", false},
		{"COMMIT;", "", "", false},
		{"SAVEPOINT invalid_name;", "", "", false},
	}

	for _, tt := range tests {
		name, op, ok := parseSavepoint(tt.sql)
		if ok != tt.wantOk {
			t.Errorf("parseSavepoint(%q) ok = %v, want %v", tt.sql, ok, tt.wantOk)
			continue
		}
		if ok {
			if name != tt.wantName {
				t.Errorf("parseSavepoint(%q) name = %q, want %q", tt.sql, name, tt.wantName)
			}
			if op != tt.wantOp {
				t.Errorf("parseSavepoint(%q) op = %q, want %q", tt.sql, op, tt.wantOp)
			}
		}
	}
}

func TestValidator_MultipleTeardownsFail(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "test", Directory: "./test/__test__", PreExec: Ptr("SAVEPOINT __pgmi_0__;"), PostExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;")},
		{Ordinal: 2, StepType: "teardown", Directory: "./test/__test__", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: Ptr("RELEASE SAVEPOINT __pgmi_0__;")},
		{Ordinal: 3, StepType: "teardown", Directory: "./test/__test__", PreExec: Ptr("ROLLBACK TO SAVEPOINT __pgmi_0__;"), PostExec: Ptr("RELEASE SAVEPOINT __pgmi_0__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if result.Valid {
		t.Error("multiple teardowns for same directory should fail")
	}
}

func TestValidator_ReleaseWithEmptyStack(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "teardown", Directory: "./test/__test__", PostExec: Ptr("RELEASE SAVEPOINT __pgmi_0__;")},
	}

	v := NewSavepointValidator()
	result := v.Validate(rows)
	if result.Valid {
		t.Error("RELEASE with empty stack should fail")
	}

	found := false
	for _, err := range result.Errors {
		if err.Invariant == "NestingOrder" && err.Message == "RELEASE with empty stack" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected NestingOrder error for empty stack, got: %v", result.Errors)
	}
}
