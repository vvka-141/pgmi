package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var configCmd = &cobra.Command{
	Use:   "config [path]",
	Short: "Run the connection wizard and save to pgmi.yaml",
	Long: `Launch the connection wizard and write the result to pgmi.yaml.

  pgmi config              In the current directory
  pgmi config ./project    In ./project

The wizard handles local, Azure Entra ID, AWS IAM, and Google Cloud SQL
auth. Requires an interactive terminal — for CI, write pgmi.yaml by hand.`,
	Args: usageArgs(cobra.MaximumNArgs(1)),
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

	if !isInteractive() {
		return fmt.Errorf("%w: config requires an interactive terminal\nfor CI, write pgmi.yaml by hand or use PG* environment variables", pgmi.ErrUsage)
	}

	existingCfg, err := config.Load(targetDir)
	switch {
	case errors.Is(err, config.ErrConfigNotFound):
		// No existing config — safe to create without prompting.
	case err != nil:
		// pgmi.yaml exists but is malformed; prompt before clobbering it
		// rather than silently overwriting (and losing) the user's file.
		fmt.Fprintf(os.Stderr, "pgmi.yaml exists but could not be parsed: %v\n", err)
		if !promptContinue("Overwrite the unparseable pgmi.yaml?") {
			return wizardCancelled("config", false)
		}
	case existingCfg != nil:
		fmt.Fprintln(os.Stderr, "pgmi.yaml exists.")
		if !promptContinue("Overwrite connection settings?") {
			return wizardCancelled("config", false)
		}
	}

	var opts []wizards.WizardOption
	if existingCfg != nil {
		opts = append(opts, wizards.WithInitialConfig(existingCfg.Connection))
	}
	connResult, err := startConnectionWizard(opts...)
	if err != nil {
		return fmt.Errorf("connection wizard failed: %w", err)
	}
	if connResult.Cancelled {
		return wizardCancelled("config", connResult.Interrupted)
	}

	if err := saveConnectionToConfig(targetDir, &connResult.Config, connResult.MaintenanceDatabase); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Wrote %s\n", filepath.Join(targetDir, "pgmi.yaml"))
	offerSavePgpass(&connResult.Config, connResult.MaintenanceDatabase)
	return nil
}
