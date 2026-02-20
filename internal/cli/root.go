package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "pgmi",
	Short: "PostgreSQL-native execution fabric",
	Long: asciiLogo + `

pgmi loads project files and parameters into session-scoped temporary tables,
then executes your deploy.sql—where you control transactions, execution order,
and all deployment logic using PostgreSQL's procedural languages.

No proprietary DSL. No migration framework. Just SQL in control.

Philosophy: Minimal interference, maximum empowerment.

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
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		printVersionInfo()
		return nil
	}
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().Bool("help", false, "Help for pgmi")
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
