package cli

import (
	"fmt"
	"os"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/spf13/cobra"
)

var templatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "Manage project templates",
	Long: `List and describe available project templates.

Templates provide different starting points for your pgmi projects,
from simple learning structures to production-ready deployments.`,
}

var templatesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available templates",
	Long:  `List all available project templates with descriptions.`,
	RunE:  runTemplatesList,
}

var templatesDescribeCmd = &cobra.Command{
	Use:               "describe <template_name>",
	Short:             "Show detailed information about a template",
	Long:              `Show detailed information about a specific template including structure and features.`,
	Args:              RequireTemplateName,
	ValidArgsFunction: completeTemplateNames,
	RunE:              runTemplatesDescribe,
}

func init() {
	rootCmd.AddCommand(templatesCmd)
	templatesCmd.AddCommand(templatesListCmd)
	templatesCmd.AddCommand(templatesDescribeCmd)
}

func runTemplatesList(cmd *cobra.Command, args []string) error {
	templates, err := scaffold.ListTemplates()
	if err != nil {
		return fmt.Errorf("failed to list templates: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Available templates:")
	fmt.Fprintln(os.Stderr)

	// Template descriptions
	descriptions := getTemplateDescriptions()

	for _, t := range templates {
		desc, ok := descriptions[t]
		if !ok {
			desc = TemplateDescription{
				Short: "No description available",
				Long:  "",
			}
		}

		fmt.Fprintf(os.Stderr, "  %-12s %s\n", t, desc.Short)
		if desc.Long != "" {
			fmt.Fprintf(os.Stderr, "               %s\n", desc.Long)
		}
		if desc.BestFor != "" {
			fmt.Fprintf(os.Stderr, "               Best for: %s\n", desc.BestFor)
		}
		fmt.Fprintln(os.Stderr)
	}

	fmt.Fprintln(os.Stderr, "Use: pgmi init <project_name> --template <template_name>")
	return nil
}

func runTemplatesDescribe(cmd *cobra.Command, args []string) error {
	templateName := args[0]

	if !scaffold.IsValidTemplate(templateName) {
		templates, _ := scaffold.ListTemplates()
		return fmt.Errorf("template '%s' not found. Available templates: %v\n\nUse 'pgmi templates list' to see all templates", templateName, templates)
	}

	// Get template description
	descriptions := getTemplateDescriptions()
	desc, ok := descriptions[templateName]
	if !ok {
		return fmt.Errorf("no description available for template '%s'", templateName)
	}

	// Print detailed description
	fmt.Fprintf(os.Stderr, "Template: %s\n", templateName)
	fmt.Fprintf(os.Stderr, "Description: %s\n", desc.Short)
	if desc.Long != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", desc.Long)
	}

	if len(desc.Structure) > 0 {
		fmt.Fprintln(os.Stderr, "\nStructure:")
		for _, item := range desc.Structure {
			fmt.Fprintf(os.Stderr, "  %s\n", item)
		}
	}

	if len(desc.Features) > 0 {
		fmt.Fprintln(os.Stderr, "\nFeatures:")
		for _, feature := range desc.Features {
			fmt.Fprintf(os.Stderr, "  - %s\n", feature)
		}
	}

	if desc.BestFor != "" {
		fmt.Fprintf(os.Stderr, "\nBest for: %s\n", desc.BestFor)
	}

	fmt.Fprintf(os.Stderr, "\nUsage:\n  pgmi init myproject --template %s\n", templateName)

	return nil
}

// TemplateDescription contains metadata about a template
type TemplateDescription struct {
	Short     string
	Long      string
	Structure []string
	Features  []string
	BestFor   string
}

// getTemplateDescriptions returns descriptions for all templates
func getTemplateDescriptions() map[string]TemplateDescription {
	return map[string]TemplateDescription{
		"basic": {
			Short:   "Simple structure for learning",
			Long:    "A minimal template with just the essentials to get started with pgmi.",
			Structure: []string{
				"├── deploy.sql",
				"└── migrations/",
				"    └── 001_example.sql",
			},
			Features: []string{
				"Single migrations directory",
				"Basic deploy.sql orchestrator",
				"Minimal setup for quick starts",
			},
			BestFor: "Quick prototypes, learning pgmi basics",
		},
		"advanced": {
			Short:   "Advanced patterns with savepoint protection",
			Long:    "Template demonstrating advanced PostgreSQL patterns including savepoint management and complex orchestration.",
			Structure: []string{
				"├── deploy.sql",
				"└── migrations/",
				"    └── 001_example.sql",
			},
			Features: []string{
				"Savepoint protection",
				"Advanced error handling",
				"Complex phase lifecycle",
				"Transaction safety patterns",
			},
			BestFor: "Complex deployments requiring advanced transactional safety",
		},
	}
}
