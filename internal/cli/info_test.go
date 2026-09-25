package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectTemplate_Advanced(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lib", "api"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := detectTemplate(dir); got != "advanced" {
		t.Errorf("detectTemplate() = %q, want %q", got, "advanced")
	}
}

func TestDetectTemplate_Basic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte("SELECT 1;"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := detectTemplate(dir); got != "basic" {
		t.Errorf("detectTemplate() = %q, want %q", got, "basic")
	}
}

func TestDetectTemplate_Unknown(t *testing.T) {
	dir := t.TempDir()
	if got := detectTemplate(dir); got != "unknown" {
		t.Errorf("detectTemplate() = %q, want %q", got, "unknown")
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

	if info.Template != "basic" {
		t.Errorf("template = %q, want %q", info.Template, "basic")
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
