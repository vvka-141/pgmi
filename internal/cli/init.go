package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var initCmd = &cobra.Command{
	Use:   "init [target_path]",
	Short: "Scaffold a new pgmi project",
	Long: `Scaffold a new pgmi project (deploy.sql, directory layout, README).

  pgmi init                  Scaffold in the current directory (wizard if TTY)
  pgmi init ./demo           Scaffold in ./demo
  pgmi init ./demo -t basic  Skip the wizard, use the basic template

Without a TTY and without -t, pgmi init uses the minimal template.

Templates:
  minimal   ` + scaffold.TemplateSummary("minimal") + `
  basic     ` + scaffold.TemplateSummary("basic") + `
  advanced  ` + scaffold.TemplateSummary("advanced") + `

Target directory must be empty (pgmi.yaml, .env and .git are tolerated).
Run ` + "`pgmi templates list`" + ` for full template descriptions.`,
	Args:              usageArgs(cobra.MaximumNArgs(1)),
	ValidArgsFunction: completeDirectories,
	RunE:              runInit,
}

var initTemplate string

func init() {
	rootCmd.AddCommand(initCmd)

	initCmd.Flags().StringVarP(&initTemplate, "template", "t", "minimal", "Template to use (minimal, basic, advanced)")

	mustRegisterCompletion(initCmd, "template", completeTemplateFlag)
}

func runInit(cmd *cobra.Command, args []string) error {
	explicitPath := len(args) > 0
	verbose := getVerboseFlag(cmd)
	templateFlagChanged := cmd.Flags().Changed("template")

	selectedTemplate := initTemplate
	setupConnection := false
	var connResult wizards.ConnectionResult
	var targetPath string

	if isInteractive() && !templateFlagChanged {
		prefill := ""
		if explicitPath {
			prefill = args[0]
		}

		result, err := runInitWizard(prefill)
		if err != nil {
			return fmt.Errorf("init wizard failed: %w", err)
		}
		if result.Cancelled {
			return wizardCancelled("init", result.Interrupted)
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
			return fmt.Errorf("%w: %s", pgmi.ErrUsage, reason)
		}
	}

	projectName := defaultProjectName(targetPath)

	if !scaffold.IsValidTemplate(selectedTemplate) {
		templates, _ := scaffold.ListTemplates()
		return fmt.Errorf("%w: unknown template %q (available: %s)\nrun `pgmi templates list` for descriptions", pgmi.ErrUsage, selectedTemplate, strings.Join(templates, ", "))
	}

	// Create scaffolder
	v, _, _ := resolveVersionInfo()
	scaffolder := scaffold.NewScaffolder(verbose).WithVersion(v)

	_, statErr := os.Stat(filepath.Join(targetPath, config.ConfigFileName))
	keptConfig := statErr == nil

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

	if setupConnection {
		if err := saveConnectionToConfig(targetPath, &connResult.Config, connResult.MaintenanceDatabase); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: could not save connection: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "Wrote %s\n", filepath.Join(targetPath, "pgmi.yaml"))
			offerSavePgpass(&connResult.Config, connResult.MaintenanceDatabase)
		}
	}

	// A kept pgmi.yaml that names a database is the target: -d would override it.
	dbName := "mydb"
	switch {
	case setupConnection && connResult.Config.Database != "":
		dbName = connResult.Config.Database
	case keptConfig:
		if cfg, err := config.Load(targetPath); err == nil && cfg.Connection.Database != "" {
			dbName = ""
		}
	}

	fmt.Fprint(os.Stderr, nextSteps(targetPath, dbName, selectedTemplate))

	return nil
}

// nextSteps is what the user reads after a scaffold. It names the agent
// guidance the scaffold wrote, so the user commits it instead of discarding it.
func nextSteps(targetPath, dbName, template string) string {
	deploy := "pgmi deploy " + shellQuote(targetPath)
	if dbName != "" {
		deploy += " -d " + shellQuote(dbName)
	}
	if template == "minimal" {
		return fmt.Sprintf(`
Next:
  %s

deploy.sql runs every .sql file in path order. Add files, then change its
query to decide what runs. Run "pgmi ai" for the docs that match this version.
`, deploy)
	}
	deploy += " --param env=dev"
	if template == "advanced" {
		deploy += `

For a shared database use env=test or env=prod, with the passwords in
PGMI_PARAM_* environment variables or a --params-file.`
	}
	return fmt.Sprintf(`
Next:
  %s

AGENTS.md and .agents/skills/ tell an AI assistant how this project works;
commit them, and edit them as your conventions change. Run "pgmi ai" for the
docs that match this pgmi version.
`, deploy)
}

// shellQuote is s as a POSIX shell word: as is when it is plain, else in
// single quotes.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func managedCloudCaveat(template string) string {
	if template != "advanced" {
		return ""
	}
	return "Managed-cloud note: the advanced template needs PostgreSQL 18 or newer and the\n" +
		"uuid-ossp, pgcrypto, pg_trgm and hstore extensions. No superuser required: deploy\n" +
		"as a role with CREATEROLE and CREATEDB. Details:\n" +
		"https://github.com/vvka-141/pgmi/blob/main/docs/PRODUCTION.md#managed-cloud-postgresql"
}

// isInitBlocked checks if the target directory has non-pgmi files that would block init.
// Returns (true, reason) if blocked, (false, "") if safe to proceed.
func isInitBlocked(targetPath string) (bool, string) {
	blocking := scaffold.BlockingEntries(targetPath)
	if len(blocking) == 0 {
		return false, ""
	}

	absPath, _ := filepath.Abs(targetPath)
	return true, fmt.Sprintf("directory \"%s\" is not empty (blocking: %v)\nremove the files or scaffold elsewhere: pgmi init ./new-project", absPath, blocking)
}

// defaultProjectName names a project after its directory, resolving "." and
// ".." through the working directory.
func defaultProjectName(targetPath string) string {
	if abs, err := filepath.Abs(targetPath); err == nil {
		return filepath.Base(abs)
	}
	return "project"
}
