package scaffold_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Python gateway and api.accept_matches answer the same question about the
// same header, so they have to agree. PGMI-222 taught the SQL side that q=0 is
// a refusal (RFC 9110 12.5.1) and left this one splitting the parameters off
// and discarding them, which read an explicit refusal as acceptance.
func TestMCPGateway_AcceptQValues(t *testing.T) {
	python := findPython(t)

	workDir := t.TempDir()
	gateway, err := os.ReadFile(filepath.Join("templates", "advanced", "tools", "mcp-gateway.py"))
	if err != nil {
		t.Fatalf("read gateway source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "gateway.py"), gateway, 0o600); err != nil {
		t.Fatalf("write gateway copy: %v", err)
	}
	// _accepts_json is pure, but the module resolves psycopg at import time.
	writePsycopgStub(t, workDir)

	cases := []struct {
		accept string
		want   bool
	}{
		{"application/json", true},
		{"application/*", true},
		{"*/*", true},
		{"application/json;q=0.5", true},
		{"text/html, application/json;q=0.9", true},
		{"APPLICATION/JSON", true},
		{"application/json ; q=0.3", true},
		{"application/json;q=0", false},
		{"application/json;q=0.0", false},
		{"*/*;q=0", false},
		{"application/*;q=0", false},
		{"text/html", false},
		{"application/json-patch+json", false},
		{"text/html, application/json;q=0", false},
	}

	inputs := make([]string, len(cases))
	for i, c := range cases {
		inputs[i] = c.accept
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		t.Fatalf("marshal inputs: %v", err)
	}

	script := `import json, sys
import gateway
print(json.dumps([gateway._accepts_json(a) for a in json.loads(sys.argv[1])]))`

	cmd := exec.Command(python, "-c", script, string(encoded))
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"DATABASE_URL=postgresql://stub:stub@127.0.0.1:1/stub",
		"PYTHONPATH="+workDir,
		"PYTHONDONTWRITEBYTECODE=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run gateway probe: %v\n%s", err, out)
	}

	var got []bool
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &got); err != nil {
		t.Fatalf("decode probe output %q: %v", out, err)
	}
	if len(got) != len(cases) {
		t.Fatalf("expected %d results, got %d", len(cases), len(got))
	}
	for i, c := range cases {
		if got[i] != c.want {
			t.Errorf("_accepts_json(%q) = %v, want %v", c.accept, got[i], c.want)
		}
	}
}
