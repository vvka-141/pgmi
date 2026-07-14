package cli

import (
	"cmp"
	"fmt"
	"os"
	"slices"

	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/scaffold"
)

// templateRank orders templates by what we recommend, not by name. basic is the
// starting point; advanced is a reference app to read, not the default choice.
func templateRank(name string) int {
	switch name {
	case "basic":
		return 0
	case "advanced":
		return 1
	default:
		return 2
	}
}

var templatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "List or describe project templates",
	Long: `List or describe the project templates that ` + "`pgmi init`" + ` can scaffold.

  pgmi templates list
  pgmi templates describe basic`,
}

var templatesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available templates",
	Long:  `Print the names and one-line descriptions of all bundled templates.`,
	RunE:  runTemplatesList,
}

var templatesDescribeCmd = &cobra.Command{
	Use:   "describe <template_name>",
	Short: "Show the structure and features of a template",
	Long:  `Show the directory layout, features, and intended use of one template.`,
	Example: `  pgmi templates describe basic
  pgmi templates describe advanced`,
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

	descriptions := getTemplateDescriptions()

	// Recommended order, not alphabetical: listing "advanced" first would make the
	// showcase look like the default choice, which is the opposite of the advice.
	slices.SortStableFunc(templates, func(a, b string) int {
		return cmp.Compare(templateRank(a), templateRank(b))
	})

	for _, t := range templates {
		desc, ok := descriptions[t]
		if !ok {
			desc = templateDescription{Short: "(no description)"}
		}
		fmt.Fprintf(os.Stdout, "%-12s %s\n", t, desc.Short)
		if desc.BestFor != "" {
			fmt.Fprintf(os.Stdout, "%-12s   for: %s\n", "", desc.BestFor)
		}
	}

	fmt.Fprintln(os.Stderr, "\nScaffold one with: pgmi init <path> -t <template>")
	return nil
}

func runTemplatesDescribe(cmd *cobra.Command, args []string) error {
	templateName := args[0]

	if !scaffold.IsValidTemplate(templateName) {
		templates, _ := scaffold.ListTemplates()
		return fmt.Errorf("unknown template %q (available: %v)\nrun `pgmi templates list` for descriptions", templateName, templates)
	}

	descriptions := getTemplateDescriptions()
	desc, ok := descriptions[templateName]
	if !ok {
		return fmt.Errorf("no description registered for template %q", templateName)
	}

	fmt.Fprintf(os.Stdout, "%s — %s\n", templateName, desc.Short)
	if desc.Long != "" {
		fmt.Fprintf(os.Stdout, "\n%s\n", desc.Long)
	}

	if len(desc.Structure) > 0 {
		fmt.Fprintln(os.Stdout, "\nStructure:")
		for _, item := range desc.Structure {
			fmt.Fprintf(os.Stdout, "  %s\n", item)
		}
	}

	if len(desc.Features) > 0 {
		fmt.Fprintln(os.Stdout, "\nFeatures:")
		for _, feature := range desc.Features {
			fmt.Fprintf(os.Stdout, "  - %s\n", feature)
		}
	}

	if desc.BestFor != "" {
		fmt.Fprintf(os.Stdout, "\nFor: %s\n", desc.BestFor)
	}

	fmt.Fprintf(os.Stderr, "\nScaffold with: pgmi init <path> -t %s\n", templateName)

	return nil
}

// templateDescription contains metadata about a template
type templateDescription struct {
	Short     string
	Long      string
	Structure []string
	Features  []string
	BestFor   string
}

// getTemplateDescriptions returns descriptions for all templates
func getTemplateDescriptions() map[string]templateDescription {
	return map[string]templateDescription{
		"basic": {
			Short: "Linear migrations, minimal structure",
			Long:  "A small starter project: deploy.sql executes migrations in order, reads project.json for metadata, and branches on environment. No metadata system, no idempotency tracking, no advanced libraries.",
			Structure: []string{
				"├── deploy.sql",
				"├── project.json",
				"├── pgmi.yaml",
				"├── README.md",
				"├── migrations/",
				"│   ├── 001_users.sql",
				"│   └── 002_user_crud.sql",
				"└── __test__/",
				"    ├── _setup.sql",
				"    └── test_user_crud.sql",
			},
			Features: []string{
				"Linear migration ordering by filename",
				"Environment-aware deployment (--param env=production skips dev seeding)",
				"Non-SQL project data loading (project.json via pgmi_source_view)",
				"Test scaffolding via __test__/ (CALL pgmi_test())",
			},
			BestFor: "Start here. Deploying SQL — small, explicit, any managed provider",
		},
		"advanced": {
			Short: "Reference app: SQL-native REST/RPC/MCP, multi-tenant RLS, audit",
			Long:  "A full SQL-native application built on pgmi, for you to read, trim, and own — not a framework to adopt wholesale. <pgmi-meta> sortKeys for explicit phase ordering, idempotency tracking, role hierarchy (owner/admin/api/customer), API-key and identity-based authentication, REST/RPC/MCP routing, and an api.handler registry. Targets stock PostgreSQL — no proprietary extensions. Not the recommended starting point: begin with basic and take what you need from here.",
			Structure: []string{
				"├── deploy.sql",
				"├── pgmi.yaml",
				"├── session.xml",
				"├── README.md",
				"├── ARCHITECTURE.md",
				"├── lib/",
				"│   ├── core/        (entity standards, foundation, attached properties)",
				"│   ├── common/      (cast, text, encoding helpers)",
				"│   └── api/         (REST/RPC/MCP routing, handler registry, queue)",
				"├── membership/      (users, organizations, identities, API keys, RLS)",
				"├── api/             (your handler implementations + examples.sql)",
				"└── tools/",
			},
			Features: []string{
				"<pgmi-meta> sortKeys for multi-phase execution ordering",
				"Idempotency via script-UUID tracking in internal.deployment_script",
				"Role hierarchy: owner → admin → api → customer",
				"API-key and multi-provider identity authentication (JWT validated at gateway)",
				"REST/RPC/MCP routing with handler registry (api.handler)",
				"Row-level security policies on membership tables",
				"Targets stock PostgreSQL — superuser required for DDL event trigger (see docs/PRODUCTION.md for managed-cloud workaround)",
			},
			BestFor: "Seeing what pgmi enables. Not a starting point — read it, take what you need",
		},
	}
}
