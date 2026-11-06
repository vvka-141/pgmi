package scaffold

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
)

func TestMinimalTemplate_WritesOnlyItsThreeFiles(t *testing.T) {
	dir := scaffoldInto(t, "minimal")
	var got []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		got = append(got, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".pgmiignore", "deploy.sql", "hello_world.sql"}; !slices.Equal(got, want) {
		t.Errorf("minimal scaffold wrote %q, want exactly %q (no skills, no pgmi.yaml)", got, want)
	}
}

// The README's "entire model in one file" and the minimal deploy.sql are the
// same loop, so a reader recognises the generated file.
func TestMinimalDeploySQL_MatchesTheREADMELoop(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	deploy, err := fs.ReadFile(templatesFS, "templates/minimal/deploy.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, example, _ := strings.Cut(string(readme), "```sql\n-- deploy.sql\n")
	example, _, _ = strings.Cut(example, "```")
	readmeLoop, deployLoop := doBlock(example), doBlock(string(deploy))
	if readmeLoop == "" || readmeLoop != deployLoop {
		t.Errorf("README loop and minimal deploy.sql differ:\nREADME:\n%s\ndeploy.sql:\n%s", readmeLoop, deployLoop)
	}
}

func doBlock(sql string) string {
	start := strings.Index(sql, "DO $$")
	end := strings.Index(sql, "END $$;")
	if start < 0 || end < start {
		return ""
	}
	return sql[start : end+len("END $$;")]
}

// Only dev seeds the basic template's demo data, so a committed pgmi.yaml must
// not supply env: a CI deploy that forgets it has to fail.
func TestScaffoldedPgmiYAMLSetsNoEnv(t *testing.T) {
	for _, template := range []string{"basic", "advanced"} {
		cfg, err := config.Load(scaffoldInto(t, template))
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := cfg.Params["env"]; ok {
			t.Errorf("%s pgmi.yaml sets env=%q", template, v)
		}
	}
}
