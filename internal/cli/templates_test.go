package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
)

func TestGetTemplateDescriptions(t *testing.T) {
	descriptions := getTemplateDescriptions()

	// Verify all expected templates have descriptions
	expectedTemplates := []string{"basic", "advanced"}

	for _, template := range expectedTemplates {
		desc, ok := descriptions[template]
		if !ok {
			t.Errorf("missing description for template '%s'", template)
			continue
		}

		// Verify description has required fields
		if desc.Short == "" {
			t.Errorf("template '%s' missing short description", template)
		}

		if desc.BestFor == "" {
			t.Errorf("template '%s' missing BestFor field", template)
		}

		if len(desc.Features) == 0 {
			t.Errorf("template '%s' has no features listed", template)
		}

		if len(desc.Structure) == 0 {
			t.Errorf("template '%s' has no structure listed", template)
		}
	}
}

// The CLI must not contradict the website. The settled positioning is: basic is
// the starting point, advanced is a reference app, and BOTH are production-capable
// — advanced is more infrastructure, not a higher safety tier. Selling advanced as
// "for: Production deployments" says the opposite, and says it in the one place a
// user looks while deciding.
func TestTemplateDescriptions_DoNotSellAdvancedAsTheProductionTier(t *testing.T) {
	descriptions := getTemplateDescriptions()
	advanced := descriptions["advanced"]

	for _, field := range []struct{ name, text string }{
		{"Short", advanced.Short},
		{"Long", advanced.Long},
		{"BestFor", advanced.BestFor},
	} {
		if strings.Contains(strings.ToLower(field.text), "production deployments") {
			t.Errorf("advanced.%s implies basic is not for production: %q", field.name, field.text)
		}
	}

	if strings.Contains(strings.ToLower(advanced.BestFor), "start here") {
		t.Error("advanced.BestFor must not claim 'start here' — that is basic's position")
	}
}

// Listing advanced first would make the showcase read as the default choice.
func TestTemplatesListRecommendsBasicFirst(t *testing.T) {
	if templateRank("basic") >= templateRank("advanced") {
		t.Error("basic must be listed before advanced: it is the recommended starting point")
	}
}

func TestTemplateDescriptionContent(t *testing.T) {
	descriptions := getTemplateDescriptions()

	tests := []struct {
		name                string
		expectedShort       string
		minFeatures         int
		minStructureEntries int
	}{
		{
			name:                "basic",
			expectedShort:       "Two layers: run-once tables, re-run views and functions",
			minFeatures:         3,
			minStructureEntries: 2,
		},
		{
			name:                "advanced",
			expectedShort:       "Reference app: SQL-native REST/RPC/MCP, multi-tenant RLS, audit",
			minFeatures:         3,
			minStructureEntries: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, ok := descriptions[tt.name]
			if !ok {
				t.Fatalf("template '%s' not found", tt.name)
			}

			if desc.Short != tt.expectedShort {
				t.Errorf("template '%s' short description mismatch:\nwant: %s\ngot:  %s",
					tt.name, tt.expectedShort, desc.Short)
			}

			if len(desc.Features) < tt.minFeatures {
				t.Errorf("template '%s' has %d features, expected at least %d",
					tt.name, len(desc.Features), tt.minFeatures)
			}

			if len(desc.Structure) < tt.minStructureEntries {
				t.Errorf("template '%s' has %d structure entries, expected at least %d",
					tt.name, len(desc.Structure), tt.minStructureEntries)
			}
		})
	}
}

func TestTemplateSummary_OneSource(t *testing.T) {
	templates, err := scaffold.ListTemplates()
	if err != nil {
		t.Fatal(err)
	}
	wizard := map[string]string{}
	for _, tpl := range wizards.DefaultTemplates() {
		wizard[tpl.Name] = tpl.Description
	}
	for _, name := range templates {
		short := getTemplateDescriptions()[name].Short
		if short == "" {
			t.Errorf("%s: `pgmi templates list` has no description", name)
		}
		if wizard[name] != short {
			t.Errorf("%s: wizard says %q, templates list says %q", name, wizard[name], short)
		}
		if !strings.Contains(initCmd.Long, short) {
			t.Errorf("%s: `pgmi init --help` does not show %q", name, short)
		}
	}
}

// structurePaths turns a Structure tree ("├── lib/", "│   └── core/  (note)")
// into the paths it names, relative to the project root.
func structurePaths(lines []string) []string {
	var paths, stack []string
	for _, line := range lines {
		runes := []rune(line)
		i := strings.Index(line, "── ")
		if i < 0 {
			continue
		}
		depth := len([]rune(line[:i])) / 4
		name := strings.Fields(string(runes[len([]rune(line[:i]))+3:]))[0]
		stack = append(stack[:depth], strings.TrimSuffix(name, "/"))
		paths = append(paths, strings.Join(stack, "/"))
	}
	return paths
}

func TestTemplateStructureNamesOnlyRealPaths(t *testing.T) {
	for name, desc := range getTemplateDescriptions() {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			if err := scaffold.NewScaffolder(false).CreateProject("p", name, dir); err != nil {
				t.Fatal(err)
			}
			paths := structurePaths(desc.Structure)
			if len(paths) == 0 {
				t.Fatal("no paths parsed from Structure")
			}
			for _, p := range paths {
				if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
					t.Errorf("`pgmi templates describe %s` names %s, which a scaffolded project does not have", name, p)
				}
			}
		})
	}
}
