package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/scaffold"
)

func TestUnusedTestsNotice(t *testing.T) {
	scan := func(tmpl string) scanner.Result {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "p")
		if err := scaffold.NewScaffolder(false).CreateProject("p", tmpl, dir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "__test__"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "__test__", "test_fails.sql"), []byte("DO $$ BEGIN RAISE EXCEPTION 'no'; END $$;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r, err := scanner.NewScanner(checksum.New()).ScanDirectory(dir)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if got := unusedTestsNotice(scan("minimal"), 0); !strings.Contains(got, "1 test file(s) loaded; deploy.sql never calls pgmi_test()") {
		t.Errorf("minimal with a __test__ file: %q", got)
	}
	if got := unusedTestsNotice(scan("basic"), 1); got != "" {
		t.Errorf("basic calls pgmi_test(): %q", got)
	}
}
