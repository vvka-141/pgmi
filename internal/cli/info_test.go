package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
)

func TestDetectTemplate_ReadsTheScaffoldMarker(t *testing.T) {
	for _, tmpl := range []string{"minimal", "basic", "advanced"} {
		dir := filepath.Join(t.TempDir(), "p")
		if err := scaffold.NewScaffolder(false).CreateProject("p", tmpl, dir); err != nil {
			t.Fatal(err)
		}
		if got := detectTemplate(dir); got != tmpl {
			t.Errorf("%s project: detectTemplate() = %q", tmpl, got)
		}
	}
	bare := t.TempDir()
	if err := os.WriteFile(filepath.Join(bare, "deploy.sql"), []byte("SELECT 1;"), 0644); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"bare deploy.sql": bare, "empty": t.TempDir()} {
		if got := detectTemplate(dir); got != "unknown" {
			t.Errorf("%s: detectTemplate() = %q, want unknown", name, got)
		}
	}
}

func TestRunInfo_BasicProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte("SELECT 1;"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "migrations"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "migrations", "001.sql"), []byte("CREATE TABLE t(id int);"), 0644); err != nil {
		t.Fatal(err)
	}

	infoFlags.jsonOutput = true
	defer func() { infoFlags.jsonOutput = false }()

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runInfo(infoCmd, []string{dir})
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runInfo() error: %v", err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	r.Close()

	var info projectInfo
	if err := json.Unmarshal(buf[:n], &info); err != nil {
		t.Fatalf("json.Unmarshal: %v\nraw: %s", err, buf[:n])
	}

	if info.Template != "unknown" {
		t.Errorf("template = %q, want %q", info.Template, "unknown")
	}
	if !info.DeploySQL {
		t.Error("deploySql should be true")
	}
	if info.SQLFiles < 1 {
		t.Errorf("sqlFiles = %d, want >= 1", info.SQLFiles)
	}
}

func TestShowBanner_SuppressedByEnv(t *testing.T) {
	t.Setenv("PGMI_NO_BANNER", "1")
	if showBanner() {
		t.Error("showBanner() should return false when PGMI_NO_BANNER is set")
	}
}

func TestShowBanner_OnlyOnATerminalWithoutOptOut(t *testing.T) {
	orig := isInteractive
	t.Cleanup(func() { isInteractive = orig })

	for _, tt := range []struct {
		name        string
		interactive bool
		noBanner    string
		want        bool
	}{
		{"terminal, no opt-out", true, "", true},
		{"terminal, PGMI_NO_BANNER", true, "1", false},
		{"no terminal", false, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isInteractive = func() bool { return tt.interactive }
			t.Setenv("PGMI_NO_BANNER", tt.noBanner)
			if got := showBanner(); got != tt.want {
				t.Errorf("showBanner() = %v, want %v", got, tt.want)
			}
		})
	}
}
