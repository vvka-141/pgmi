package scaffold

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const gatewayDemoHeader = "Demo code from pgmi. Do not copy this file into the project as is."

var gatewayLanguages = []string{"python", "typescript", "go", "csharp", "rust"}

func isGatewaySource(name string) bool {
	switch filepath.Ext(name) {
	case ".py", ".ts", ".go", ".cs", ".rs", ".toml", ".csproj", ".mod":
		return true
	}
	return name == "requirements.txt"
}

func TestAdvancedScaffoldShipsTheGatewayExamples(t *testing.T) {
	dir := scaffoldInto(t, "advanced")

	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"`gateways/README.md`", "read `gateways/` first", "`<path>/gateways/`"} {
		if !strings.Contains(string(agents), want) {
			t.Errorf("AGENTS.md does not contain %s", want)
		}
	}

	readme, err := os.ReadFile(filepath.Join(dir, "gateways", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gateways", "PROTOCOL.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "gateways", "go", "go.mod")); err != nil {
		t.Errorf("gateways/go must be its own module: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gateways", "go", "go.mod"+scaffoldSuffix)); err == nil {
		t.Errorf("go.mod%s must be written as go.mod", scaffoldSuffix)
	}

	for _, lang := range gatewayLanguages {
		root := filepath.Join(dir, "gateways", lang)
		if !strings.Contains(string(readme), "["+lang+"/]("+lang+"/)") {
			t.Errorf("gateways/README.md does not list %s/", lang)
		}
		var sources, adapt int
		named := false
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !isGatewaySource(d.Name()) {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sources++
			if !strings.Contains(string(content), gatewayDemoHeader) {
				t.Errorf("%s lacks the demo-code header", path)
			}
			adapt += strings.Count(string(content), "ADAPT")
			named = named || strings.Contains(string(content), `"pgmi-gateway-`+lang+`"`)
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		if sources == 0 {
			t.Errorf("gateways/%s has no source files", lang)
		}
		if adapt == 0 {
			t.Errorf("gateways/%s marks no ADAPT place", lang)
		}
		if !named {
			t.Errorf("gateways/%s never sets application_name pgmi-gateway-%s", lang, lang)
		}
	}
}

// TestGatewayProblemCodesAreNamespaced fails on a problem code a reference
// gateway emits outside pgmi.*, membership.* or core.*: clients branch on the
// namespace, and the SQL side already holds it (PGMI-486).
func TestGatewayProblemCodesAreNamespaced(t *testing.T) {
	quoted := regexp.MustCompile(`["'][a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+["']`)
	namespaced := regexp.MustCompile(`^["'](?:pgmi|membership|core)\.`)
	roleID := regexp.MustCompile(`^["'](?:agent|org)\.`)
	sources := map[string]bool{".go": true, ".py": true, ".ts": true, ".cs": true, ".rs": true}
	checked := 0
	err := fs.WalkDir(GetTemplatesFS(), "templates/advanced/gateways", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !sources[filepath.Ext(path)] {
			return err
		}
		body, err := fs.ReadFile(GetTemplatesFS(), path)
		if err != nil {
			return err
		}
		for n, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(strings.ToLower(line), "code") {
				continue
			}
			for _, code := range quoted.FindAllString(line, -1) {
				if roleID.MatchString(code) {
					continue
				}
				checked++
				if !namespaced.MatchString(code) {
					t.Errorf("%s:%d: problem code %s is outside pgmi.*, membership.* and core.*", path, n+1, code)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("found no problem code in the gateways: the scan checks nothing")
	}
}
