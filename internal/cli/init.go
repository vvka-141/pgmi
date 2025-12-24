package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init <target_path>",
	Short: "Initialize a new pgmi project",
	Long: `Initialize a pgmi project into the specified directory.

The init command initializes a pgmi project with:
- deploy.sql orchestrator script
- Directory structure for SQL files
- README with usage instructions

Target directory must be empty or non-existent.

Examples:
  pgmi init .                    # Initialize in current directory
  pgmi init ./myproject          # Initialize in ./myproject
  pgmi init /absolute/path       # Initialize at absolute path

Available templates:
  basic    - Simple structure for learning (migrations/)
  advanced - Production-ready with metadata-driven deployment

Use 'pgmi templates list' to see all available templates with descriptions.`,
	Args: cobra.MinimumNArgs(0),
	RunE: runInit,
}

var (
	initTemplate string
	initList     bool
)

func init() {
	rootCmd.AddCommand(initCmd)

	initCmd.Flags().StringVarP(&initTemplate, "template", "t", "basic", "Template to use (basic, advanced)")
	initCmd.Flags().BoolVar(&initList, "list", false, "List available templates")
}

func runInit(cmd *cobra.Command, args []string) error {
	// Handle --list flag
	if initList {
		return runTemplatesList(cmd, args)
	}

	// Require target path if not listing
	if len(args) == 0 {
		return fmt.Errorf("target path required\n\nUsage: pgmi init <target_path> [flags]\n\nExamples:\n  pgmi init .           # Current directory\n  pgmi init ./myproject # Subdirectory\n\nUse 'pgmi init --list' to see available templates")
	}

	targetPath := args[0]
	
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
	verbose := getVerboseFlag(cmd)

	// Validate template
	templates, err := scaffold.ListTemplates()
	if err != nil {
		return fmt.Errorf("failed to list templates: %w", err)
	}

	validTemplate := false
	for _, t := range templates {
		if t == initTemplate {
			validTemplate = true
			break
		}
	}

	if !validTemplate {
		return fmt.Errorf("invalid template '%s'. Available templates: %v\n\nUse 'pgmi templates list' for detailed descriptions", initTemplate, templates)
	}

	// Create scaffolder
	scaffolder := scaffold.NewScaffolder(verbose)

	// Create project
	if err := scaffolder.CreateProject(projectName, initTemplate, targetPath); err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}

	// Display file tree
	tree, err := scaffold.BuildFileTree(targetPath)
	if err != nil {
		// Non-fatal - just skip tree display
		fmt.Fprintf(os.Stderr, "\n✓ Project initialized successfully in '%s' using template '%s'\n\n", targetPath, initTemplate)
	} else {
		fmt.Fprintf(os.Stderr, "\n✓ Project initialized successfully using template '%s'\n\n", initTemplate)
		fmt.Fprintln(os.Stderr, "Created structure:")
		fmt.Fprint(os.Stderr, tree)
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
