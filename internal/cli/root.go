package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:           "pgmi",
	Short:         "PostgreSQL-native execution fabric",
	SilenceErrors: true,
	Long: asciiLogo + `

pgmi loads project files and parameters into session-scoped temporary tables,
then executes your deploy.sql—where you control transactions, execution order,
and all deployment logic using PostgreSQL's procedural languages.

No proprietary DSL. No migration framework. Just SQL in control.

Philosophy: Minimal interference, maximum empowerment.

Exit Codes:
  0   - Success
  1   - General error (deployment or test failed)
  2   - CLI usage error (invalid arguments or flags)
  3   - Panic or unexpected system error
  10  - Invalid configuration or parameters
  11  - Database connection failed
  12  - User denied overwrite approval
  13  - SQL execution failed
  14  - deploy.sql not found
  130 - Interrupted (SIGINT / Ctrl-C)

Environment Variables:
  Connection (PostgreSQL libpq standard):
    PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE, PGSSLMODE
    PGAPPNAME          application_name for pg_stat_activity (default: "pgmi")
    PGCONNECT_TIMEOUT  connection timeout in seconds
    PGSSLCERT, PGSSLKEY, PGSSLROOTCERT, PGSSLPASSWORD
    PGPASSFILE         path to .pgpass (default: ~/.pgpass or %APPDATA%\postgresql\pgpass.conf)
    DATABASE_URL       full connection string (Heroku/Rails convention)

  Cloud authentication:
    AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET  (Azure Entra ID)
    AWS_REGION, AWS_DEFAULT_REGION                         (AWS RDS IAM)

  pgmi behavior:
    PGMI_CONNECTION_STRING  explicit connection string (overrides PG* vars)
    PGMI_NON_INTERACTIVE    set to "1" to disable TUI wizards
    CI                      any non-empty value disables TUI wizards
    NO_COLOR                any non-empty value disables ANSI colors (wizard still runs)`,
	SilenceUsage: true,
}

// Execute runs the root command
func Execute() error {
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
