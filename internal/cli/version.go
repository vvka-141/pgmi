package cli

import (
	"fmt"
	"os"
	"runtime"

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

// printVersionInfo prints version information.
// Pure version string goes to stdout for pipeline consumption (e.g., pgmi version | cut -d' ' -f2).
// Decorative content (logo, commit, platform) goes to stderr.
func printVersionInfo() {
	fmt.Fprintln(os.Stderr, asciiLogo)
	fmt.Fprintln(os.Stderr)
	// Machine-parseable version to stdout (pure semver for pipelines)
	fmt.Printf("pgmi %s\n", version)
	// Decorative info to stderr
	fmt.Fprintf(os.Stderr, "Commit: %s, Built: %s, Platform: %s/%s\n", commit, date, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintln(os.Stderr, "PostgreSQL deployment tool")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Repository: https://github.com/vvka-141/pgmi")
}
