package cli

import (
	"strings"
	"testing"
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

	if !strings.Contains(strings.ToLower(advanced.BestFor+advanced.Long), "not a starting point") &&
		!strings.Contains(strings.ToLower(advanced.BestFor+advanced.Long), "not the recommended starting point") {
		t.Error("advanced must say plainly that it is not the starting point")
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
			expectedShort:       "Linear migrations, minimal structure",
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
