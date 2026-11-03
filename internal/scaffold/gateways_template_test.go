package scaffold

import (
	"io/fs"
	"os"
	"path/filepath"
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
