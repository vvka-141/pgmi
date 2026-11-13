package cli

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// templateRank orders templates from the smallest up, not by name: minimal is
// the default, basic the recommended structure, advanced a reference app to
// read, not the default choice.
func templateRank(name string) int {
	switch name {
	case "minimal":
		return 0
	case "basic":
		return 1
	case "advanced":
		return 2
	default:
		return 3
	}
}

var templatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "List or describe project templates",
	Long: `List or describe the project templates that ` + "`pgmi init`" + ` can scaffold.

  pgmi templates list
  pgmi templates describe basic`,
	Args: usageArgs(cobra.NoArgs),
	RunE: showHelp,
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
		return fmt.Errorf("%w: unknown template %q (available: %s)\nrun `pgmi templates list` for descriptions", pgmi.ErrUsage, templateName, strings.Join(templates, ", "))
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
		"minimal": {
			Short: scaffold.TemplateSummary("minimal"),
			Long:  "The smallest pgmi project: deploy.sql loops over every SQL file in path order and executes it inside one transaction, hello_world.sql raises a notice, and .pgmiignore keeps parameter files and backup copies out of the session. No tables, ledger, tests, parameters, pgmi.yaml or agent skills. Start here to build everything your own way.",
			Structure: []string{
				"├── .pgmiignore",
				"├── deploy.sql",
				"└── hello_world.sql",
			},
			Features: []string{
				"deploy.sql is the README's \"entire model in one file\" loop",
				"Prints Executing: <path> before each file",
			},
			BestFor: "Building your own deployment from nothing",
		},
		"basic": {
			Short: scaffold.TemplateSummary("basic"),
			Long:  "A small project in two layers. physical/ holds tables, keys and indexes in run-once files; semantic/ holds the views and writer functions, re-run on every deploy. deploy.sql is short enough to read in one sitting: it refuses a run-once file edited after it ran, runs the files in path order, seeds a demo project on env=dev and gates the commit on the tests.",
			Structure: []string{
				"├── deploy.sql",
				"├── project.json",
				"├── pgmi.yaml",
				"├── README.md",
				"├── physical/",
				"│   ├── 001_project.sql",
				"│   ├── 002_task.sql",
				"│   └── __test__/",
				"├── semantic/",
				"│   ├── project.sql",
				"│   ├── task.sql",
				"│   └── __test__/",
				"└── __test__/",
				"    └── test_function_privileges.sql",
			},
			Features: []string{
				"Run-once structure with a ledger; a run-once file edited, moved or removed after it ran refuses the deploy",
				"Views as the read surface, writer functions with refusal functions as the only writers",
				"Environment-aware deployment (--param env=production skips dev seeding)",
				"Tests per layer in __test__/ (CALL pgmi_test()) gate the commit",
			},
			BestFor: "Start here. Deploying SQL — small, explicit, any managed provider",
		},
		"advanced": {
			Short: scaffold.TemplateSummary("advanced"),
			Long:  "A full SQL-native application built on pgmi, for you to read, trim, and own — not a framework to adopt wholesale. <pgmi-meta> sortKeys for explicit phase ordering, idempotency tracking, role hierarchy (owner/admin/api/kernel), identity-based authentication, REST/RPC/MCP routing, and an api.handler registry. Targets stock PostgreSQL — no proprietary extensions. More infrastructure than basic, not a higher safety tier — either is production-capable.",
			Structure: []string{
				"├── deploy.sql",
				"├── deploy/          (the setup deploy.sql runs by name: roles, schemas, ledger)",
				"├── pgmi.yaml",
				"├── session.xml      (deployment parameters and their defaults)",
				"├── README.md",
				"├── ARCHITECTURE.md",
				"├── AGENTS.md        (routing table for coding agents)",
				"├── .agents/",
				"│   └── skills/      (project-owned agent skills)",
				"├── data/            (permissions and roles: authorization.json)",
				"├── lib/",
				"│   ├── core/        (entity standards, catalogs, change tracking)",
				"│   ├── common/      (cast, text, encoding helpers)",
				"│   ├── internal/    (environment, job run log, deployment views)",
				"│   └── api/         (REST/RPC/MCP routing, handler registry, exchange log)",
				"├── membership/      (users, organizations, identities, RLS)",
				"├── api/             (your handler implementations + examples.sql)",
				"├── gateways/        (reference gateways in five languages; never deployed)",
				"└── __test__/",
			},
			Features: []string{
				"<pgmi-meta> sortKeys for multi-phase execution ordering",
				"Idempotency via script-UUID tracking in internal.deployment_script",
				"Roles: owner, admin, api and a NOLOGIN kernel role; each executes exactly its list",
				"Sign-in through any OIDC provider at the gateway; the database keeps the sessions behind opaque handles",
				"REST/RPC/MCP routing with handler registry (api.handler)",
				"Row-level security policies on membership tables",
				"Targets stock PostgreSQL 18+ — no superuser, no event trigger; needs CREATEROLE + CREATE EXTENSION",
			},
			BestFor: "SQL-native application stack — scaffold, read, trim to fit",
		},
	}
}
