package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/pathrule"
	"github.com/vvka-141/pgmi/internal/files/scanner"
)

// The advanced template keeps client code and build output out of the
// session; both templates keep parameter files, which hold secrets, out of the
// session and out of git.
func TestTemplatesShipPgmiignoreWhereTheyNeedIt(t *testing.T) {
	advanced, err := os.ReadFile(filepath.Join(scaffoldInto(t, "advanced"), pathrule.PgmiignoreFile))
	if err != nil {
		t.Fatalf("advanced scaffold must ship %s: %v", pathrule.PgmiignoreFile, err)
	}
	for _, want := range []string{"gateways/", "target/", "bin/", "obj/", "dist/"} {
		if !strings.Contains(string(advanced), "\n"+want+"\n") {
			t.Errorf("advanced %s must exclude %s", pathrule.PgmiignoreFile, want)
		}
	}
}

func TestTemplatesKeepParameterFilesOutOfSessionAndGit(t *testing.T) {
	for _, template := range []string{"minimal", "basic", "advanced"} {
		t.Run(template, func(t *testing.T) {
			dir := scaffoldInto(t, template)
			ignoreFiles := []string{".gitignore", pathrule.PgmiignoreFile}
			if template == "minimal" {
				ignoreFiles = []string{pathrule.PgmiignoreFile}
			}
			for _, name := range ignoreFiles {
				content, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatalf("%s scaffold must ship %s: %v", template, name, err)
				}
				for _, want := range []string{"*.params", "*.params.*", "*.env"} {
					if !strings.Contains(string(content), "\n"+want+"\n") {
						t.Errorf("%s %s must exclude %s", template, name, want)
					}
				}
			}

			for _, secret := range []string{"secrets.env", "prod.params", "ci.params.local"} {
				if err := os.WriteFile(filepath.Join(dir, secret), []byte("database_admin_password=x\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := scanner.NewScanner(checksum.New()).ScanDirectory(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range result.Files {
				for _, secret := range []string{"secrets.env", "prod.params", "ci.params.local"} {
					if strings.HasSuffix(f.Path, secret) {
						t.Errorf("%s is loaded into the session", f.Path)
					}
				}
			}
		})
	}
}

func TestTemplatesKeepBackupCopiesOutOfTheSession(t *testing.T) {
	for _, template := range []string{"minimal", "basic", "advanced"} {
		t.Run(template, func(t *testing.T) {
			dir := scaffoldInto(t, template)
			shipped, err := scanner.NewScanner(checksum.New()).ScanDirectory(dir)
			if err != nil {
				t.Fatal(err)
			}
			backups := []string{"project (copy).sql", "project.sql~", "project (1).sql", "project - Copy.sql",
				"project - Copy (2).sql", "project copy.sql", "project copy 2.sql"}
			for _, name := range backups {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			result, err := scanner.NewScanner(checksum.New()).ScanDirectory(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range result.Files {
				for _, name := range backups {
					if strings.HasSuffix(f.Path, name) {
						t.Errorf("the backup %s is loaded into the session", f.Path)
					}
				}
			}
			if len(result.Files) != len(shipped.Files) {
				t.Errorf("%d files loaded beside the backups, want the %d the template ships", len(result.Files), len(shipped.Files))
			}
		})
	}
}
