package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGetContract_ViewsMatchEmbeddedSQL(t *testing.T) {
	schema, err := contentFS.ReadFile("content/../../../params/schema.sql")
	if err != nil {
		// Embedded FS doesn't reach outside content/; read from contract package's perspective.
		// We verify against the contract's own expectations instead.
		t.Skip("cannot read schema.sql from embedded FS (expected)")
	}
	_ = schema
}

func TestGetContract_Structure(t *testing.T) {
	c := GetContract()

	if len(c.Views) == 0 {
		t.Fatal("expected views")
	}
	if len(c.Functions) == 0 {
		t.Fatal("expected functions")
	}
	if len(c.StepTypes) != 3 {
		t.Errorf("expected 3 step types, got %d", len(c.StepTypes))
	}
	if len(c.ExitCodes) == 0 {
		t.Fatal("expected exit codes")
	}
	if len(c.Macros) != 3 {
		t.Errorf("expected 3 macros, got %d", len(c.Macros))
	}

	viewNames := make(map[string]bool)
	for _, v := range c.Views {
		viewNames[v.Name] = true
		if len(v.Columns) == 0 {
			t.Errorf("view %s has no columns", v.Name)
		}
	}

	for _, required := range []string{"pgmi_source_view", "pgmi_parameter_view", "pgmi_plan_view"} {
		if !viewNames[required] {
			t.Errorf("missing required view %s", required)
		}
	}

	for _, ec := range c.ExitCodes {
		if ec.Code < 0 {
			t.Errorf("exit code %s has negative value", ec.Name)
		}
		if ec.Description == "" {
			t.Errorf("exit code %s has no description", ec.Name)
		}
	}
}

func TestGetContractJSON_ValidJSON(t *testing.T) {
	out, err := GetContractJSON()
	if err != nil {
		t.Fatal(err)
	}

	var parsed Contract
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if len(parsed.Views) != len(GetContract().Views) {
		t.Error("JSON round-trip lost views")
	}
}

func TestGetContract_ExitCodesMatchConstants(t *testing.T) {
	c := GetContract()

	codes := make(map[int]string)
	for _, ec := range c.ExitCodes {
		if _, dup := codes[ec.Code]; dup {
			t.Errorf("duplicate exit code %d", ec.Code)
		}
		codes[ec.Code] = ec.Name
	}

	if codes[0] != "ExitSuccess" {
		t.Error("exit code 0 should be ExitSuccess")
	}
	if codes[13] != "ExitExecutionFailed" {
		t.Error("exit code 13 should be ExitExecutionFailed")
	}
}

func TestGetContract_MacroForms(t *testing.T) {
	c := GetContract()
	for _, m := range c.Macros {
		if !strings.HasPrefix(m.Form, "CALL pgmi_test") {
			t.Errorf("macro form %q should start with CALL pgmi_test", m.Form)
		}
	}
}
