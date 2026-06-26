package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// callTool drives a single tools/call through the MCP server and returns the
// decoded result object plus whether it was an error result.
func callTool(t *testing.T, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	srv := buildMCPServer("0.0.0-test")

	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name +
		`","arguments":` + string(argsJSON) + `}}`

	var out strings.Builder
	if err := srv.Serve(context.Background(), strings.NewReader(req), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	var resp struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
		t.Fatalf("decode response %q: %v", out.String(), err)
	}
	if resp.Error != nil {
		t.Fatalf("protocol error: %s", resp.Error.Message)
	}
	isErr, _ := resp.Result["isError"].(bool)
	return resp.Result, isErr
}

// resultText returns the first text content block of a tool result.
func resultText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("no content in result: %v", result)
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}

func TestServeToolsListHasAllTools(t *testing.T) {
	srv := buildMCPServer("v")
	var out strings.Builder
	err := srv.Serve(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &out)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range resp.Result.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{
		"deploy", "init", "metadata_plan", "metadata_validate", "templates_list",
		"ai_overview", "ai_skills", "ai_skill", "ai_contract",
	} {
		if !got[want] {
			t.Errorf("tools/list missing %q (got %v)", want, got)
		}
	}
}

func TestServeAISkill(t *testing.T) {
	result, isErr := callTool(t, "ai_skill", map[string]any{"name": "pgmi-sql"})
	if isErr {
		t.Fatalf("ai_skill errored: %s", resultText(t, result))
	}
	if !strings.Contains(resultText(t, result), "pgmi-sql") {
		t.Errorf("ai_skill content unexpected: %.80s", resultText(t, result))
	}
}

func TestServeAISkillMissingName(t *testing.T) {
	result, isErr := callTool(t, "ai_skill", map[string]any{})
	if !isErr {
		t.Fatalf("expected error result for missing name")
	}
	if !strings.Contains(resultText(t, result), "name is required") {
		t.Errorf("unexpected error text: %s", resultText(t, result))
	}
}

func TestServeAIContractIsJSON(t *testing.T) {
	result, isErr := callTool(t, "ai_contract", map[string]any{})
	if isErr {
		t.Fatalf("ai_contract errored: %s", resultText(t, result))
	}
	var contract map[string]any
	if err := json.Unmarshal([]byte(resultText(t, result)), &contract); err != nil {
		t.Fatalf("ai_contract is not valid JSON: %v", err)
	}
}

func TestServeTemplatesList(t *testing.T) {
	result, isErr := callTool(t, "templates_list", map[string]any{})
	if isErr {
		t.Fatalf("templates_list errored: %s", resultText(t, result))
	}
	text := resultText(t, result)
	if !strings.Contains(text, "basic") || !strings.Contains(text, "advanced") {
		t.Errorf("templates_list missing basic/advanced: %s", text)
	}
}

func TestServeInitAndMetadataPlan(t *testing.T) {
	dir := t.TempDir() + "/proj"

	result, isErr := callTool(t, "init", map[string]any{"path": dir, "template": "basic"})
	if isErr {
		t.Fatalf("init errored: %s", resultText(t, result))
	}

	planResult, isErr := callTool(t, "metadata_plan", map[string]any{"path": dir})
	if isErr {
		t.Fatalf("metadata_plan errored: %s", resultText(t, planResult))
	}
	var plan MetadataPlanResult
	if err := json.Unmarshal([]byte(resultText(t, planResult)), &plan); err != nil {
		t.Fatalf("metadata_plan not valid JSON: %v", err)
	}
	if plan.TotalFiles == 0 {
		t.Errorf("expected scaffolded files in plan, got 0")
	}
}

func TestServeMetadataValidate(t *testing.T) {
	dir := t.TempDir() + "/proj"
	if _, isErr := callTool(t, "init", map[string]any{"path": dir, "template": "advanced"}); isErr {
		t.Fatalf("init advanced failed")
	}
	result, isErr := callTool(t, "metadata_validate", map[string]any{"path": dir})
	if isErr {
		t.Fatalf("metadata_validate errored: %s", resultText(t, result))
	}
	var v MetadataValidateResult
	if err := json.Unmarshal([]byte(resultText(t, result)), &v); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if !v.ValidationPassed {
		t.Errorf("freshly scaffolded advanced template should validate: %+v", v)
	}
}

func TestServeDeployRejectsMissingDatabase(t *testing.T) {
	// No database → config validation fails before any connection attempt.
	result, isErr := callTool(t, "deploy", map[string]any{
		"path":       "/tmp/whatever",
		"connection": "postgresql://localhost/postgres",
	})
	if !isErr {
		t.Fatalf("expected error result for missing database")
	}
	if !strings.Contains(resultText(t, result), "DatabaseName is required") {
		t.Errorf("unexpected error: %s", resultText(t, result))
	}
}

func TestServeDeployRejectsBadTimeout(t *testing.T) {
	result, isErr := callTool(t, "deploy", map[string]any{
		"path":       "/tmp/whatever",
		"connection": "postgresql://localhost/postgres",
		"database":   "x",
		"timeout":    "not-a-duration",
	})
	if !isErr {
		t.Fatalf("expected error result for bad timeout")
	}
	if !strings.Contains(resultText(t, result), "invalid timeout") {
		t.Errorf("unexpected error: %s", resultText(t, result))
	}
}
