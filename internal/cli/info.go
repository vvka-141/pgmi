package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/ui"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var infoFlags struct {
	jsonOutput bool
}

var infoCmd = &cobra.Command{
	Use:   "info [path]",
	Short: "Show project structure summary (no database required)",
	Long: `Inspect a pgmi project directory without connecting to a database.

Shows file counts by directory, template type, deploy.sql presence,
test coverage, and metadata usage.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInfo,
}

func init() {
	rootCmd.AddCommand(infoCmd)
	infoCmd.Flags().BoolVar(&infoFlags.jsonOutput, "json", false, "Emit structured JSON to stdout")
}

type projectInfo struct {
	Path         string            `json:"path"`
	Template     string            `json:"template"`
	DeploySQL    bool              `json:"deploySql"`
	ConfigFile   string            `json:"configFile"`
	TotalFiles   int               `json:"totalFiles"`
	SQLFiles     int               `json:"sqlFiles"`
	TestFiles    int               `json:"testFiles"`
	MetadataWith int               `json:"metadataWith"`
	Directories  map[string]int    `json:"directories"`
}

func runInfo(cmd *cobra.Command, args []string) error {
	sourcePath := "."
	if len(args) > 0 {
		sourcePath = args[0]
	}

	absPath, err := filepath.Abs(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	info := projectInfo{
		Path:        absPath,
		Directories: make(map[string]int),
	}

	// Check deploy.sql
	s := scanner.NewScanner(checksum.New())
	if err := s.ValidateDeploySQL(sourcePath); err == nil {
		info.DeploySQL = true
	}

	// Check pgmi.yaml
	_, cfgErr := config.Load(sourcePath)
	switch {
	case cfgErr == nil:
		info.ConfigFile = "ok"
	case errors.Is(cfgErr, config.ErrConfigNotFound):
		info.ConfigFile = "absent"
	default:
		info.ConfigFile = fmt.Sprintf("error: %v", cfgErr)
	}

	// Detect template type
	info.Template = detectTemplate(sourcePath)

	// Scan files
	scanResult, err := s.ScanDirectory(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to scan directory: %w", err)
	}

	info.TotalFiles = len(scanResult.Files)
	for _, f := range scanResult.Files {
		dir := f.Directory
		if dir == "" {
			dir = "."
		}
		info.Directories[dir]++

		if pgmi.IsSQLExtension(f.Extension) {
			info.SQLFiles++
		}
		if pgmi.IsTestPath(f.Path) {
			info.TestFiles++
		}
		if f.Metadata != nil {
			info.MetadataWith++
		}
	}

	if infoFlags.jsonOutput {
		b, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return fmt.Errorf("json marshal error: %w", err)
		}
		fmt.Println(string(b))
		return nil
	}

	printProjectInfo(info)
	return nil
}

func detectTemplate(sourcePath string) string {
	if _, err := os.Stat(filepath.Join(sourcePath, "lib", "api")); err == nil {
		return "advanced"
	}
	if _, err := os.Stat(filepath.Join(sourcePath, "deploy.sql")); err == nil {
		return "basic"
	}
	return "unknown"
}

func printProjectInfo(info projectInfo) {
	w, done := ui.PageWriter()
	defer done()

	fmt.Fprintf(w, "%s %s\n", ui.Bold("Project:"), info.Path)
	fmt.Fprintf(w, "%s %s\n", ui.Bold("Template:"), info.Template)

	deploySQLStatus := ui.FailIcon() + " missing"
	if info.DeploySQL {
		deploySQLStatus = ui.SuccessIcon() + " present"
	}
	fmt.Fprintf(w, "%s %s\n", ui.Bold("deploy.sql:"), deploySQLStatus)
	fmt.Fprintf(w, "%s %s\n", ui.Bold("pgmi.yaml:"), info.ConfigFile)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "%s\n", ui.Bold("Files"))
	fmt.Fprintf(w, "  Total:    %d\n", info.TotalFiles)
	fmt.Fprintf(w, "  SQL:      %d\n", info.SQLFiles)
	fmt.Fprintf(w, "  Tests:    %d\n", info.TestFiles)
	fmt.Fprintf(w, "  Metadata: %d / %d\n", info.MetadataWith, info.TotalFiles)
	fmt.Fprintln(w)

	if len(info.Directories) > 0 {
		fmt.Fprintf(w, "%s\n", ui.Bold("Directories"))
		dirs := make([]string, 0, len(info.Directories))
		for d := range info.Directories {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			fmt.Fprintf(w, "  %-30s %d files\n", d, info.Directories[d])
		}
	}
}

// ensure FileScanner is compatible
var _ pgmi.FileScanner = (*scanner.Scanner)(nil)
