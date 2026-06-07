package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/tui"
)

var rootCmd = &cobra.Command{
	Use:           "pgmi",
	Short:         "PostgreSQL-native deployment driver",
	SilenceErrors: true,
	Long: `PostgreSQL-native deployment driver.

pgmi loads your project files and CLI parameters into pg_temp tables, then
runs deploy.sql against the target database. You write the deployment in SQL
and PL/pgSQL — transactions, ordering, idempotency, retries are under your
control. pgmi handles connection, parameters, and exit codes.

  pgmi init demo                Scaffold a starter project
  pgmi deploy ./demo -d mydb    Run deploy.sql against mydb
  pgmi help deploy              Full deploy options

Connection follows libpq: PGHOST, PGUSER, PGPASSWORD, PGDATABASE, PGSSLMODE,
.pgpass. Run ` + "`pgmi help deploy`" + ` for cloud-auth flags and exit codes.`,
	SilenceUsage: true,
	Run:          runRoot,
}

// runRoot handles `pgmi` invoked with no subcommand. In an interactive
// terminal it prints a brief identity splash; in non-interactive contexts
// (CI, pipes) or when PGMI_NO_BANNER is set, it falls back to standard
// help output so scripts that capture this surface are not affected.
func runRoot(cmd *cobra.Command, args []string) {
	if showBanner() {
		fmt.Fprintln(os.Stderr, asciiLogo)
		fmt.Fprintln(os.Stderr, "PostgreSQL-native deployment driver.")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "  pgmi init demo                Scaffold a starter project")
		fmt.Fprintln(os.Stderr, "  pgmi deploy ./demo -d mydb    Run deploy.sql against mydb")
		fmt.Fprintln(os.Stderr, "  pgmi --help                   Full reference")
		return
	}
	_ = cmd.Help()
}

// showBanner reports whether the identity splash may be drawn. Banners
// belong on attention surfaces only — never in pipes, CI, or when the
// user has explicitly opted out via PGMI_NO_BANNER.
func showBanner() bool {
	if os.Getenv("PGMI_NO_BANNER") != "" {
		return false
	}
	return tui.IsInteractive()
}

// Execute runs the root command
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Enable verbose output for all commands")
}

// getVerboseFlag safely retrieves the verbose flag value
func getVerboseFlag(cmd *cobra.Command) bool {
	verbose, err := cmd.Flags().GetBool("verbose")
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: failed to get verbose flag: %v\n", err)
		return false
	}
	return verbose
}
