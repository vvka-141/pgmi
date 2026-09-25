package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

const dupID = "550e8400-e29b-41d4-a716-446655440000"

func metaFile(id, key string) string {
	return "/*\n<pgmi-meta id=\"" + id + "\" idempotent=\"true\">\n<sortKeys><key>" + key + "</key></sortKeys>\n</pgmi-meta>\n*/\nSELECT 1;\n"
}

// The listing is the command's output: `pgmi metadata plan p | grep x` must
// see it (PGMI-383).
func TestMetadataPlan_ListingGoesToStdout(t *testing.T) {
	dir := createTestProject(t, map[string]string{"deploy.sql": "SELECT 1;", "migrations/001.sql": "SELECT 1;"})
	out, err := withRootArgs(t, "metadata", "plan", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "./migrations/001.sql") {
		t.Errorf("plan listing not on stdout; got %q", out)
	}
}

// Every failure exits 10 and, with --json, still prints an envelope.
func TestMetadataCommands_FailuresExit10WithJSONEnvelope(t *testing.T) {
	duplicate := map[string]string{
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": metaFile(dupID, "001"),
		"migrations/002.sql": metaFile(dupID, "002"),
	}
	malformed := map[string]string{
		"deploy.sql":         "SELECT 1;",
		"migrations/001.sql": metaFile("not-a-uuid", "001"),
	}
	for _, tt := range []struct {
		name  string
		files map[string]string
		args  []string
	}{
		{"validate duplicate ids", duplicate, []string{"metadata", "validate", "--json"}},
		{"validate malformed id", malformed, []string{"metadata", "validate", "--json"}},
		{"plan malformed id", malformed, []string{"metadata", "plan", "--json"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := createTestProject(t, tt.files)
			out, err := withRootArgs(t, append(tt.args, dir)...)
			if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
				t.Errorf("exit code %d, want %d: %v", got, pgmi.ExitConfigError, err)
			}
			var envelope map[string]any
			if jerr := json.Unmarshal([]byte(out), &envelope); jerr != nil {
				t.Fatalf("stdout is not a JSON envelope: %v\n%s", jerr, out)
			}
			if tt.name != "validate duplicate ids" && envelope["error"] == nil {
				t.Errorf("envelope has no error field: %s", out)
			}
		})
	}
}
