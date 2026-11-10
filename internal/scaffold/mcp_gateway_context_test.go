package scaffold_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Without an OAuth token the MCP gateway forwards exactly one identity input,
// the connection handle, from Authorization: Bearer cn_... or X-Connection.
// An identity, email, tenant or session header from the client, or any other
// bearer value, must never reach the database's MCP context.
func TestMCPGateway_ForwardsOnlyTheConnection(t *testing.T) {
	python := findPython(t)

	workDir := t.TempDir()
	gateway, err := os.ReadFile(filepath.Join("../../shared/templates", "advanced", "gateways", "python", "gateway.py"))
	if err != nil {
		t.Fatalf("read gateway source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "gateway.py"), gateway, 0o600); err != nil {
		t.Fatalf("write gateway copy: %v", err)
	}
	writeGatewayDependencies(t, workDir)

	cases := []struct {
		name    string
		headers map[string]string
		want    map[string]string
	}{
		{"none", map[string]string{}, map[string]string{}},
		{"connection", map[string]string{"X-Connection": "cn_abc"}, map[string]string{"connection": "cn_abc"}},
		{"lower case", map[string]string{"x-connection": "cn_abc"}, map[string]string{"connection": "cn_abc"}},
		{"bearer handle", map[string]string{"Authorization": "Bearer cn_def"}, map[string]string{"connection": "cn_def"}},
		{"bearer handle wins", map[string]string{"Authorization": "bearer cn_def", "X-Connection": "cn_abc"},
			map[string]string{"connection": "cn_def"}},
		{"another bearer", map[string]string{"Authorization": "Bearer eyJ.x.y"}, map[string]string{}},
		{"client-named identity and organization", map[string]string{
			"X-Connection":           "cn_abc",
			"X-User-Id":              "https://idp.example.com|someone",
			"X-User-Email":           "someone@example.com",
			"X-Tenant-Id":            "00000000-0000-4000-8000-000000000001",
			"X-Organization-Id":      "00000000-0000-4000-8000-000000000001",
			"X-Login-Session":        "ls_abc",
			"X-Organization-Session": "os_abc",
		}, map[string]string{"connection": "cn_abc"}},
		{"sessions alone", map[string]string{"X-Login-Session": "ls_abc", "X-Organization-Session": "os_abc"},
			map[string]string{}},
	}

	inputs := make([]map[string]string, len(cases))
	for i, c := range cases {
		inputs[i] = c.headers
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		t.Fatalf("marshal inputs: %v", err)
	}

	script := `import json, sys
from email.message import Message
import gateway
out = []
for h in json.loads(sys.argv[1]):
    m = Message()
    for k, v in h.items():
        m[k] = v
    out.append(gateway._handle_context(m))
print(json.dumps(out))`

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

	var got []map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &got); err != nil {
		t.Fatalf("decode probe output %q: %v", out, err)
	}
	if len(got) != len(cases) {
		t.Fatalf("expected %d results, got %d", len(cases), len(got))
	}
	for i, c := range cases {
		if len(got[i]) != len(c.want) {
			t.Errorf("%s: context = %v, want %v", c.name, got[i], c.want)
			continue
		}
		for k, v := range c.want {
			if got[i][k] != v {
				t.Errorf("%s: context = %v, want %v", c.name, got[i], c.want)
			}
		}
	}
}
