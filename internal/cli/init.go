package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/tui"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
)

var initCmd = &cobra.Command{
	Use:   "init [target_path]",
	Short: "Scaffold a new pgmi project",
	Long: `Scaffold a new pgmi project (deploy.sql, directory layout, README).

  pgmi init                  Scaffold in the current directory (wizard if TTY)
  pgmi init ./demo           Scaffold in ./demo
  pgmi init ./demo -t basic  Skip the wizard, use the basic template

Templates:
  basic     Linear migrations, minimal structure
  advanced  Metadata-driven deployment with the api/ membership/ libraries

Target directory must be empty (pgmi.yaml and .env are tolerated).
Run ` + "`pgmi templates list`" + ` for full template descriptions.`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeDirectories,
	RunE:              runInit,
}

var initTemplate string

func init() {
	rootCmd.AddCommand(initCmd)

	initCmd.Flags().StringVarP(&initTemplate, "template", "t", "basic", "Template to use (basic, advanced)")

	// Register shell completions for flag values
	_ = initCmd.RegisterFlagCompletionFunc("template", completeTemplateNames)
}

func runInit(cmd *cobra.Command, args []string) error {
	explicitPath := len(args) > 0
	verbose := getVerboseFlag(cmd)
	templateFlagChanged := cmd.Flags().Changed("template")

	selectedTemplate := initTemplate
	setupConnection := false
	var connResult wizards.ConnectionResult
	var targetPath string

	if tui.IsInteractive() && !templateFlagChanged {
		prefill := ""
		if explicitPath {
			prefill = args[0]
		}

		result, err := wizards.RunInitWizard(prefill)
		if err != nil {
			return fmt.Errorf("init wizard failed: %w", err)
		}
		if result.Cancelled {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return nil
		}

		targetPath = result.TargetDir
		selectedTemplate = result.Template
		setupConnection = result.SetupConfig
		connResult = result.ConnResult
	} else {
		// Non-interactive mode: require explicit path or default to "."
		targetPath = "."
		if explicitPath {
			targetPath = args[0]
		}

		// Fail fast if directory is not empty
		if blocked, reason := isInitBlocked(targetPath); blocked {
			return fmt.Errorf("%s", reason)
		}
	}

	// Determine project name from target path
	projectName := filepath.Base(targetPath)
	if projectName == "." || projectName == ".." {
		cwd, err := os.Getwd()
		if err == nil {
			projectName = filepath.Base(cwd)
		} else {
			projectName = "project"
		}
	}

	if !scaffold.IsValidTemplate(selectedTemplate) {
		templates, _ := scaffold.ListTemplates()
		return fmt.Errorf("unknown template %q (available: %v)\nrun `pgmi templates list` for descriptions", selectedTemplate, templates)
	}

	// Create scaffolder
	scaffolder := scaffold.NewScaffolder(verbose)

	// Create project
	if err := scaffolder.CreateProject(projectName, selectedTemplate, targetPath); err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}

	// File tree is the data the user asked for → stdout.
	// Status prose (what was done, next steps) → stderr.
	fmt.Fprintf(os.Stderr, "Wrote %s (%s template).\n", targetPath, selectedTemplate)
	if tree, err := scaffold.BuildFileTree(targetPath); err == nil {
		fmt.Fprintln(os.Stdout, tree)
	}

	if caveat := managedCloudCaveat(selectedTemplate); caveat != "" {
		fmt.Fprintln(os.Stderr, caveat)
	}

	if setupConnection && !connResult.Cancelled {
		if err := saveConnectionToConfig(targetPath, &connResult.Config, connResult.ManagementDatabase); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: could not save connection: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "Wrote %s\n", filepath.Join(targetPath, "pgmi.yaml"))
			offerSavePgpass(&connResult.Config)
		}
	}

	dbName := "mydb"
	if setupConnection && !connResult.Cancelled && connResult.Config.Database != "" {
		dbName = connResult.Config.Database
	}

	fmt.Fprintln(os.Stderr, "\nNext:")
	fmt.Fprintf(os.Stderr, "  pgmi deploy %s -d %s\n", targetPath, dbName)

	return nil
}

// managedCloudCaveat returns a post-scaffold heads-up for templates that need a
// superuser on managed clouds, or "" when the template carries no such caveat.
func managedCloudCaveat(template string) string {
	if template != "advanced" {
		return ""
	}
	return "Heads-up (advanced template on managed cloud): the superuser-only DDL\n" +
		"event trigger in lib/core/entity-standards.sql fails on providers without a\n" +
		"superuser role (AWS RDS, Cloud SQL, Supabase, Neon). Adaptation steps:\n" +
		"https://github.com/vvka-141/pgmi/blob/main/docs/PRODUCTION.md#managed-cloud-postgresql"
}

// isInitBlocked checks if the target directory has non-pgmi files that would block init.
// Returns (true, reason) if blocked, (false, "") if safe to proceed.
func isInitBlocked(targetPath string) (bool, string) {
	entries, err := os.ReadDir(targetPath)
	if err != nil {
		// Directory doesn't exist or can't be read — not blocked, CreateProject handles this
		return false, ""
	}

	var blocking []string
	for _, entry := range entries {
		if !scaffold.ManagedFiles[entry.Name()] {
			blocking = append(blocking, entry.Name())
		}
	}

	if len(blocking) == 0 {
		return false, ""
	}

	absPath, _ := filepath.Abs(targetPath)
	return true, fmt.Sprintf("directory %q is not empty (blocking: %v)\nremove the files or scaffold elsewhere: pgmi init ./new-project", absPath, blocking)
}
