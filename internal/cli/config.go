package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/tui"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
)

var configCmd = &cobra.Command{
	Use:   "config [path]",
	Short: "Interactively configure database connection",
	Long: `Launches an interactive wizard to configure the database connection in pgmi.yaml.

The wizard guides you through:
  1. Selecting your PostgreSQL provider (local, Azure, AWS, Google)
  2. Entering connection details (host, port, credentials)
  3. Testing the connection

Parameters and timeout can be edited directly in pgmi.yaml.

This command requires an interactive terminal. For non-interactive use,
create pgmi.yaml manually or use environment variables.

Examples:
  # Configure connection in current directory
  pgmi config

  # Configure connection in a specific project directory
  pgmi config ./my-project`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfig,
}

func init() {
	rootCmd.AddCommand(configCmd)
}

func runConfig(cmd *cobra.Command, args []string) error {
	targetDir := "."
	if len(args) > 0 {
		targetDir = args[0]
	}

	if !tui.IsInteractive() {
		return fmt.Errorf("config command requires an interactive terminal\n" +
			"For non-interactive use, create pgmi.yaml manually or use environment variables")
	}

	existingCfg, err := config.Load(targetDir)
	if err == nil && existingCfg != nil {
		fmt.Println("Found existing pgmi.yaml")
		if !tui.PromptContinue("Overwrite connection settings?") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	connResult, err := wizards.RunConnectionWizard()
	if err != nil {
		return fmt.Errorf("connection wizard failed: %w", err)
	}
	if connResult.Cancelled {
		fmt.Println("Cancelled.")
		return nil
	}

	if err := saveConnectionToConfig(targetDir, &connResult.Config, connResult.ManagementDatabase); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("\n✓ Connection saved to %s\n", filepath.Join(targetDir, "pgmi.yaml"))
	offerSavePgpass(&connResult.Config)
	return nil
}
