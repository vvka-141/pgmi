package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/contract"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version, commit, build date",
	Long: `Print pgmi version, commit, build date, and platform to stdout.

The first line is greppable: ` + "`pgmi version | head -1`" + ` returns just the
version string (psql --version convention).`,
	Run: func(cmd *cobra.Command, args []string) {
		printVersionInfo()
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	v, _, _ := resolveVersionInfo()
	rootCmd.Version = v
	rootCmd.SetVersionTemplate(versionTemplate())
}

// versionTemplate produces the same one-line-first output for both
// `pgmi --version` (cobra-handled) and `pgmi version`.
func versionTemplate() string {
	v, c, d := resolveVersionInfo()
	return fmt.Sprintf(
		"pgmi %s (compat %s)\nCommit: %s, Built: %s, Platform: %s/%s\n",
		v, contract.LatestVersion(), c, d, runtime.GOOS, runtime.GOARCH,
	)
}

func resolveVersionInfo() (v, c, d string) {
	v, c, d = version, commit, date

	if v != "dev" && c != "unknown" && d != "unknown" {
		return
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}

	settings := make(map[string]string)
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}

	if rev, ok := settings["vcs.revision"]; ok && c == "unknown" {
		c = rev
		if settings["vcs.modified"] == "true" {
			c += "-dirty"
		}
	}

	if t, ok := settings["vcs.time"]; ok && d == "unknown" {
		d = t
	}

	return
}

// printVersionInfo writes machine-greppable version output to stdout.
// First line is the version (psql --version convention so `pgmi version | head -1`
// returns just `pgmi 0.9.1 (compat v1)`); subsequent lines carry build metadata.
func printVersionInfo() {
	v, c, d := resolveVersionInfo()
	fmt.Printf("pgmi %s (compat %s)\n", v, contract.LatestVersion())
	fmt.Printf("Commit: %s, Built: %s, Platform: %s/%s\n", c, d, runtime.GOOS, runtime.GOARCH)
	fmt.Println("Repository: https://github.com/vvka-141/pgmi")
}
