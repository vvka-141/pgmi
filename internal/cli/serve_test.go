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

// listTools returns the tools/list descriptors from the real server.
func listTools(t *testing.T) map[string]map[string]any {
	t.Helper()
	srv := buildMCPServer("v")
	var out strings.Builder
	if err := srv.Serve(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resp struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]map[string]any{}
	for _, d := range resp.Result.Tools {
		name, _ := d["name"].(string)
		byName[name] = d
	}
	return byName
}

// The spec ties the two together: a tool declaring outputSchema must return
// conforming structuredContent, and a tool returning plain text must not declare
// one. Splitting the tools by return type keeps that promise honest.
func TestServeOutputSchemaMatchesStructuredOutput(t *testing.T) {
	tools := listTools(t)

	structured := []string{"ai_skills", "templates_list", "metadata_plan", "metadata_validate", "init", "deploy"}
	textOnly := []string{"ai_overview", "ai_skill", "ai_contract"}

	for _, name := range structured {
		if tools[name] == nil {
			t.Fatalf("tool %q not registered", name)
		}
		if tools[name]["outputSchema"] == nil {
			t.Errorf("tool %q returns a structured value and must declare outputSchema", name)
		}
	}
	for _, name := range textOnly {
		if tools[name] == nil {
			t.Fatalf("tool %q not registered", name)
		}
		if tools[name]["outputSchema"] != nil {
			t.Errorf("tool %q returns text and produces no structuredContent, so it must not declare outputSchema", name)
		}
	}
}

// The declared schema is only worth having if the tool actually emits
// structuredContent. Exercise a tool that needs neither database nor filesystem.
func TestServeStructuredToolEmitsStructuredContent(t *testing.T) {
	result, isErr := callTool(t, "templates_list", nil)
	if isErr {
		t.Fatalf("templates_list failed: %v", result)
	}
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("templates_list declares an outputSchema but sent no structuredContent: %v", result)
	}
	if _, ok := sc["templates"].([]any); !ok {
		t.Errorf("structuredContent must match the declared schema (templates array), got: %v", sc)
	}
}

func TestConfirmOverwrite(t *testing.T) {
	tests := []struct {
		name      string
		overwrite bool
		database  string
		confirm   string
		wantErr   bool
	}{
		{"no overwrite needs no confirmation", false, "prod", "", false},
		{"no overwrite ignores a stray confirmation", false, "prod", "whatever", false},
		{"overwrite without confirmation is refused", true, "prod", "", true},
		{"overwrite with a mismatched name is refused", true, "prod", "prod_test", true},
		{"overwrite with the exact name proceeds", true, "prod", "prod", false},
		{"confirmation is case-sensitive", true, "prod", "PROD", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := confirmOverwrite(tt.overwrite, tt.database, tt.confirm)
			if (err != nil) != tt.wantErr {
				t.Errorf("confirmOverwrite(%v, %q, %q) error = %v, wantErr %v",
					tt.overwrite, tt.database, tt.confirm, err, tt.wantErr)
			}
		})
	}
}

// The guard must run before anything connects: a bad echo-back on a deploy with a
// real-looking connection string must fail without touching a server.
func TestServeDeployOverwriteRequiresConfirmation(t *testing.T) {
	result, isErr := callTool(t, "deploy", map[string]any{
		"path":       t.TempDir(),
		"connection": "postgresql://nobody@127.0.0.1:1/postgres",
		"database":   "important_prod",
		"overwrite":  true,
	})
	if !isErr {
		t.Fatal("overwrite without confirmDatabaseName must be refused")
	}
	if text := resultText(t, result); !strings.Contains(text, "confirmDatabaseName") {
		t.Errorf("error should name the missing parameter, got: %s", text)
	}

	result, isErr = callTool(t, "deploy", map[string]any{
		"path":                t.TempDir(),
		"connection":          "postgresql://nobody@127.0.0.1:1/postgres",
		"database":            "important_prod",
		"overwrite":           true,
		"confirmDatabaseName": "important_prd",
	})
	if !isErr {
		t.Fatal("a mismatched confirmDatabaseName must be refused")
	}
	if text := resultText(t, result); !strings.Contains(text, "does not match") {
		t.Errorf("error should say the names disagree, got: %s", text)
	}
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
