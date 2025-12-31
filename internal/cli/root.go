package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "pgmi",
	Short: "PostgreSQL-native deployment and migration tool",
	Long: asciiLogo + `

pgmi is a PostgreSQL-first deployment tool that empowers database experts.

It loads SQL files and runtime parameters into PostgreSQL temporary tables,
then hands control to a user-provided deploy.sql script that orchestrates the
entire deployment using PostgreSQL's procedural languages.

Philosophy: Minimal interference, maximum empowerment.

Shell Completion:
  Generate shell completion scripts for bash, zsh, fish, or powershell:
    pgmi completion bash > /etc/bash_completion.d/pgmi
    pgmi completion zsh > ~/.zsh/completions/_pgmi

Exit Codes:
  0  - Success
  1  - General error (deployment or test failed)
  2  - CLI usage error (invalid arguments or flags)
  3  - Panic or unexpected system error
  10 - Invalid configuration or parameters
  11 - Database connection failed
  12 - User denied overwrite approval
  13 - SQL execution failed
  14 - deploy.sql not found`,
	SilenceUsage: true,
}

// Execute runs the root command
func Execute() error {
	// Handle --version flag before Cobra parsing for universal CLI compatibility
	for _, arg := range os.Args {
		if arg == "--version" {
			printVersionInfo()
			return nil
		}
	}
	return rootCmd.Execute()
}

func init() {
	// Global flags - verbose applies to all commands
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Enable verbose output for all commands")
}

// getVerboseFlag safely retrieves the verbose flag value
func getVerboseFlag(cmd *cobra.Command) bool {
	verbose, err := cmd.Flags().GetBool("verbose")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to get verbose flag: %v\n", err)
		return false
	}
	return verbose
}
