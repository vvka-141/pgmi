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
	Short: "Show version information",
	Run: func(cmd *cobra.Command, args []string) {
		printVersionInfo()
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

func resolveVersionInfo() (v, c, d string) {
	v, c, d = version, commit, date

	if v != "dev" {
		return
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	if info.Main.Version != "" && info.Main.Version != "(devel)" {
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

func printVersionInfo() {
	v, c, d := resolveVersionInfo()

	fmt.Println(asciiLogo)
	fmt.Println()
	fmt.Printf("pgmi %s (compat %s)\n", v, contract.LatestVersion())
	fmt.Printf("Commit: %s, Built: %s, Platform: %s/%s\n", c, d, runtime.GOOS, runtime.GOARCH)
	fmt.Println("PostgreSQL deployment tool")
	fmt.Println()
	fmt.Println("Repository: https://github.com/vvka-141/pgmi")
}
