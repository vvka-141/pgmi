package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// conformsTo validates value against the subset of JSON Schema the tool
// descriptors use: type, properties, required, items and anyOf. Unknown
// keywords are ignored and undeclared properties are permitted, matching
// draft 2020-12 defaults.
func conformsTo(schema map[string]any, value any) error {
	if branches, ok := schema["anyOf"].([]any); ok {
		var failures []string
		matched := false
		for i, raw := range branches {
			branch, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("anyOf[%d] is not a schema object", i)
			}
			if err := conformsTo(branch, value); err != nil {
				failures = append(failures, fmt.Sprintf("anyOf[%d]: %v", i, err))
				continue
			}
			matched = true
			break
		}
		if !matched {
			return fmt.Errorf("matched no branch (%s)", strings.Join(failures, "; "))
		}
	}

	declared, _ := schema["type"].(string)
	switch declared {
	case "":
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("want object, got %T", value)
		}
		for _, raw := range toSlice(schema["required"]) {
			name, _ := raw.(string)
			if _, present := obj[name]; !present {
				return fmt.Errorf("missing required property %q", name)
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, rawProp := range properties {
			present, ok := obj[name]
			if !ok {
				continue
			}
			prop, ok := rawProp.(map[string]any)
			if !ok {
				continue
			}
			if err := conformsTo(prop, present); err != nil {
				return fmt.Errorf("property %q: %w", name, err)
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("want array, got %T", value)
		}
		itemSchema, ok := schema["items"].(map[string]any)
		if !ok {
			return nil
		}
		for i, item := range items {
			if err := conformsTo(itemSchema, item); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("want string, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("want boolean, got %T", value)
		}
	case "integer":
		n, ok := value.(float64)
		if !ok {
			return fmt.Errorf("want integer, got %T", value)
		}
		if n != float64(int64(n)) {
			return fmt.Errorf("want integer, got %v", n)
		}
	default:
		return fmt.Errorf("unsupported schema type %q — extend this validator", declared)
	}
	return nil
}

func toSlice(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	}
	return nil
}

// publishedSchema returns the outputSchema a client actually receives from
// tools/list, round-tripped through JSON so the test sees the same bytes.
func publishedSchema(t *testing.T, tool string) map[string]any {
	t.Helper()

	descriptor := listTools(t)[tool]
	if descriptor == nil {
		t.Fatalf("tool %q not registered", tool)
	}
	schema, ok := descriptor["outputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("tool %q declares no outputSchema", tool)
	}
	return schema
}

var schemaDeclaringTools = []string{
	"ai_skills", "templates_list", "metadata_plan", "metadata_validate", "init", "deploy",
}

// MCP 2025-11-25 (Server/Tools, Output Schema): "Servers MUST provide
// structured results that conform to this schema." Nothing exempts a result
// carrying isError, so a schema describing only the success shape makes every
// failure a violation a validating client may reject instead of display.
func TestServeErrorResultsConformToDeclaredOutputSchema(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "proj")
	if _, isErr := callTool(t, "init", map[string]any{"path": projectDir}); isErr {
		t.Fatal("init should succeed into a fresh directory")
	}

	failures := []struct {
		tool string
		args map[string]any
	}{
		{"metadata_plan", map[string]any{"path": filepath.Join(projectDir, "nope")}},
		{"metadata_validate", map[string]any{"path": filepath.Join(projectDir, "nope")}},
		{"init", map[string]any{"path": projectDir, "template": "no-such-template"}},
		{"deploy", map[string]any{"path": projectDir, "connection": "postgresql://localhost/postgres"}},
	}

	for _, f := range failures {
		t.Run(f.tool, func(t *testing.T) {
			result, isErr := callTool(t, f.tool, f.args)
			if !isErr {
				t.Fatalf("expected an error result, got: %v", result)
			}
			structured, ok := result["structuredContent"]
			if !ok {
				// Skipping here would have excused the very regression this
				// test exists to catch: a tool that declares an outputSchema
				// and then returns a failure without a structured result is
				// already violating the MUST quoted above, before conformance
				// is even in question.
				t.Fatalf("error result carries no structuredContent, but %s declares an "+
					"outputSchema:\n%v", f.tool, result)
			}
			if err := conformsTo(publishedSchema(t, f.tool), structured); err != nil {
				t.Errorf("error result violates the declared outputSchema: %v\nresult: %v", err, structured)
			}
		})
	}
}

// ai_skills and templates_list cannot be made to fail from outside, so their
// schemas are checked against the error shape errorResult would produce. Every
// declared schema must admit it — the failure envelope is uniform across tools.
func TestServeEveryDeclaredSchemaAdmitsTheErrorShape(t *testing.T) {
	var minimal, withDiagnostics any
	mustJSON(t, `{"message":"boom","exitCode":1}`, &minimal)
	mustJSON(t, `{"message":"execution failed","exitCode":13,"sqlstate":"22012","detail":"d",
		"hint":"h","where":"w","failedFile":"./migrations/001.sql","script":"deploy.sql",
		"line":12,"column":3,"sourceLine":"SELECT 1/0;","scriptExpanded":true,
		"notices":["NOTICE:  applying"],"noticesTruncated":2}`, &withDiagnostics)

	for _, tool := range schemaDeclaringTools {
		schema := publishedSchema(t, tool)
		for name, shape := range map[string]any{"minimal": minimal, "withDiagnostics": withDiagnostics} {
			if err := conformsTo(schema, shape); err != nil {
				t.Errorf("%s: schema rejects the %s error shape: %v", tool, name, err)
			}
		}
	}
}

// The success half of the same contract: adding the error variant must not
// have loosened the schemas into accepting anything.
func TestServeSuccessResultsConformToDeclaredOutputSchema(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "proj")

	successes := []struct {
		tool string
		args map[string]any
	}{
		{"init", map[string]any{"path": projectDir}},
		{"ai_skills", nil},
		{"templates_list", nil},
		{"metadata_plan", map[string]any{"path": projectDir}},
		{"metadata_validate", map[string]any{"path": projectDir}},
	}

	for _, s := range successes {
		t.Run(s.tool, func(t *testing.T) {
			result, isErr := callTool(t, s.tool, s.args)
			if isErr {
				t.Fatalf("expected success, got error result: %v", result)
			}
			structured, ok := result["structuredContent"]
			if !ok {
				t.Fatalf("tool declares an outputSchema but sent no structuredContent: %v", result)
			}
			if err := conformsTo(publishedSchema(t, s.tool), structured); err != nil {
				t.Errorf("success result violates the declared outputSchema: %v\nresult: %v", err, structured)
			}
		})
	}
}

// A schema that admits everything would pass the tests above vacuously.
func TestConformsToRejectsMismatches(t *testing.T) {
	schema := publishedSchema(t, "init")

	var wrong any
	mustJSON(t, `{"created":"yes","path":"/tmp/x","template":"basic"}`, &wrong)
	if err := conformsTo(schema, wrong); err == nil {
		t.Error("expected a string in place of the boolean created to be rejected")
	}

	mustJSON(t, `{"path":"/tmp/x"}`, &wrong)
	if err := conformsTo(schema, wrong); err == nil {
		t.Error("expected an object missing every required property of both variants to be rejected")
	}
}

func mustJSON(t *testing.T, raw string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
}
