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
	Short: "Initialize a new pgmi project",
	Long: `Initialize a pgmi project into the specified directory.

The init command initializes a pgmi project with:
- deploy.sql orchestrator script
- Directory structure for SQL files
- README with usage instructions

When target_path is omitted, defaults to the current directory.
In an interactive terminal, a guided wizard helps select template and configure connection.
Target directory must be empty or non-existent (pgmi.yaml and .env are allowed).

Examples:
  pgmi init                      # Initialize in current directory (interactive wizard)
  pgmi init .                    # Initialize in current directory
  pgmi init ./myproject          # Initialize in ./myproject
  pgmi init /absolute/path       # Initialize at absolute path

Available templates:
  basic    - Simple structure for learning (migrations/)
  advanced - Production-ready with metadata-driven deployment

Use 'pgmi templates list' to see all available templates with descriptions.`,
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
		return fmt.Errorf("invalid template '%s'. Available templates: %v\n\nUse 'pgmi templates list' for detailed descriptions", selectedTemplate, templates)
	}

	// Create scaffolder
	scaffolder := scaffold.NewScaffolder(verbose)

	// Create project
	if err := scaffolder.CreateProject(projectName, selectedTemplate, targetPath); err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}

	// Display file tree
	tree, err := scaffold.BuildFileTree(targetPath)
	if err != nil {
		// Non-fatal - just skip tree display
		fmt.Fprintf(os.Stderr, "\n✓ Project initialized successfully in '%s' using template '%s'\n\n", targetPath, selectedTemplate)
	} else {
		fmt.Fprintf(os.Stderr, "\n✓ Project initialized successfully using template '%s'\n\n", selectedTemplate)
		fmt.Fprintln(os.Stderr, "Created structure:")
		fmt.Fprint(os.Stderr, tree)
	}

	// If user configured connection in the wizard, save it
	if setupConnection && !connResult.Cancelled {
		fmt.Fprintln(os.Stderr, "")
		if err := saveConnectionToConfig(targetPath, &connResult.Config, connResult.ManagementDatabase); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Connection setup failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "✓ Connection saved to %s\n", filepath.Join(targetPath, "pgmi.yaml"))
			offerSavePgpass(&connResult.Config)
		}
	}

	// Next steps
	fmt.Fprintln(os.Stderr, "\nNext steps:")
	if targetPath != "." {
		fmt.Fprintf(os.Stderr, "  cd %s\n", targetPath)
	}
	fmt.Fprintln(os.Stderr, "  pgmi deploy . --database mydb")
	fmt.Fprintln(os.Stderr, "  # Or with parameters:")
	fmt.Fprintln(os.Stderr, "  pgmi deploy . --database mydb --param key=value")

	return nil
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
	return true, fmt.Sprintf("directory '%s' is not empty\n\nBlocking files/directories: %v\n\npgmi init requires an empty directory to avoid overwriting existing files.\n\nOptions:\n  - Choose a different location: pgmi init ./new-project\n  - Remove existing files manually\n  - pgmi.yaml and .env are allowed", absPath, blocking)
}

