package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/tui"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
)

var configCmd = &cobra.Command{
	Use:   "config [path]",
	Short: "Interactively create or edit pgmi.yaml configuration",
	Long: `Launches an interactive wizard to create or edit pgmi.yaml configuration.

The wizard guides you through:
  1. Database connection setup (host, port, authentication)
  2. Parameter configuration (key-value pairs for deploy.sql)
  3. Timeout settings

This command requires an interactive terminal. For non-interactive use,
create pgmi.yaml manually or use environment variables.

Examples:
  # Create config in current directory
  pgmi config

  # Create config in a specific project directory
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

	// Require interactive terminal
	if !tui.IsInteractive() {
		return fmt.Errorf("config command requires an interactive terminal\n" +
			"For non-interactive use, create pgmi.yaml manually or use environment variables")
	}

	// Check if config already exists
	existingCfg, err := config.Load(targetDir)
	if err == nil && existingCfg != nil {
		fmt.Println("Found existing pgmi.yaml")
		if !tui.PromptContinue("Overwrite existing configuration?") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Run connection wizard
	connResult, err := wizards.RunConnectionWizard()
	if err != nil {
		return fmt.Errorf("connection wizard failed: %w", err)
	}
	if connResult.Cancelled {
		fmt.Println("Cancelled.")
		return nil
	}

	// Run config wizard with the connection
	cfgResult, err := wizards.RunConfigWizard(connResult.Config)
	if err != nil {
		return fmt.Errorf("config wizard failed: %w", err)
	}
	if cfgResult.Cancelled {
		fmt.Println("Cancelled.")
		return nil
	}

	// Save the config
	configPath := filepath.Join(targetDir, "pgmi.yaml")
	data, err := yaml.Marshal(cfgResult.Config)
	if err != nil {
		return fmt.Errorf("failed to serialize config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Printf("\n✓ Configuration saved to %s\n", configPath)
	offerSavePgpass(&connResult.Config)
	return nil
}
