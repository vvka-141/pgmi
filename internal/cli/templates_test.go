package cli

import (
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
			expectedShort:       "Simple structure for learning",
			minFeatures:         3,
			minStructureEntries: 2,
		},
		{
			name:                "advanced",
			expectedShort:       "Advanced patterns with savepoint protection",
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
