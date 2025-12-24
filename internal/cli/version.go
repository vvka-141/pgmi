package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Build-time variables set via ldflags
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Run: func(cmd *cobra.Command, args []string) {
		printVersionInfo()
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

// printVersionInfo prints version information
func printVersionInfo() {
	fmt.Fprintln(os.Stderr, asciiLogo)
	fmt.Fprintln(os.Stderr)
	fmt.Printf("pgmi %s\n", version)
	fmt.Fprintln(os.Stderr, "PostgreSQL deployment tool")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Repository: https://github.com/vvka-141/pgmi")
}
