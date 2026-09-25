package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
)

func TestScaffoldedFrom_ReadsTheInitLine(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(scaffold.ScaffoldedLine("0.12.2", "advanced") + "\r\nBEGIN;\n")
	if got, want := scaffoldedFrom(dir), "pgmi 0.12.2, advanced template"; got != want {
		t.Errorf("scaffoldedFrom = %q, want %q", got, want)
	}

	write("-- my own deploy script\nBEGIN;\n")
	if got := scaffoldedFrom(dir); got != "" {
		t.Errorf("a deploy.sql without the init line must report nothing, got %q", got)
	}
}
