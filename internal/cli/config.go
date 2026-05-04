package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/tui"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
)

var configCmd = &cobra.Command{
	Use:   "config [path]",
	Short: "Run the connection wizard and save to pgmi.yaml",
	Long: `Launch the connection wizard and write the result to pgmi.yaml.

  pgmi config              In the current directory
  pgmi config ./project    In ./project

The wizard handles local, Azure Entra ID, AWS IAM, and Google Cloud SQL
auth. Requires an interactive terminal — for CI, write pgmi.yaml by hand.`,
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
		return fmt.Errorf("config requires an interactive terminal\nfor CI, write pgmi.yaml by hand or use PG* environment variables")
	}

	existingCfg, err := config.Load(targetDir)
	if err == nil && existingCfg != nil {
		fmt.Fprintln(os.Stderr, "pgmi.yaml exists.")
		if !tui.PromptContinue("Overwrite connection settings?") {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return nil
		}
	}

	connResult, err := wizards.RunConnectionWizard()
	if err != nil {
		return fmt.Errorf("connection wizard failed: %w", err)
	}
	if connResult.Cancelled {
		fmt.Fprintln(os.Stderr, "Cancelled.")
		return nil
	}

	if err := saveConnectionToConfig(targetDir, &connResult.Config, connResult.ManagementDatabase); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Wrote %s\n", filepath.Join(targetDir, "pgmi.yaml"))
	offerSavePgpass(&connResult.Config)
	return nil
}
