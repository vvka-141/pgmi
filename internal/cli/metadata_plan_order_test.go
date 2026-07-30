package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// `pgmi metadata plan` exists to answer "in what order will this deploy run my
// files". For files without <pgmi-meta> it has to reproduce pgmi_plan_view's
// fallback, which is ARRAY[s.path] — the whole path.
//
// It used the base name instead, so two unmetadata'd files in different
// directories came out in the opposite order from the deploy: verified against
// PG 17.10, ./a/002_second.sql ran first while this command listed
// ./b/001_first.sql first. That is the reverse of the truth, in the one command
// someone runs when an ordering surprise sends them looking.
func TestMetadataPlanFallbackOrderMatchesPlanView(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []struct{ rel, body string }{
		{filepath.Join("a", "002_second.sql"), "SELECT 1;\n"},
		{filepath.Join("b", "001_first.sql"), "SELECT 1;\n"},
	} {
		path := filepath.Join(dir, f.rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.rel, err)
		}
	}

	result, err := planProject(dir)
	if err != nil {
		t.Fatalf("planProject: %v", err)
	}
	if len(result.Plan) != 2 {
		t.Fatalf("plan has %d entries, want 2: %+v", len(result.Plan), result.Plan)
	}

	// Byte order on the full path, which is what COLLATE "C" on ARRAY[s.path]
	// produces: "./a/..." sorts before "./b/...", the file names notwithstanding.
	if got, want := result.Plan[0].Path, "./a/002_second.sql"; got != want {
		t.Errorf("first entry is %s, want %s — the deploy runs %s first", got, want, want)
	}
	if got, want := result.Plan[1].Path, "./b/001_first.sql"; got != want {
		t.Errorf("second entry is %s, want %s", got, want)
	}

	for _, e := range result.Plan {
		if len(e.SortKeys) != 1 || e.SortKeys[0] != e.Path {
			t.Errorf("%s: fallback sort key is %v, want [%s] to match pgmi_plan_view's ARRAY[s.path]",
				e.Path, e.SortKeys, e.Path)
		}
	}
}
