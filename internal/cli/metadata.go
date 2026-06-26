package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/metadata"
	"github.com/spf13/cobra"
)

var metadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "Inspect and scaffold <pgmi-meta> blocks (no DB connection)",
	Long: `Inspect and scaffold <pgmi-meta> blocks in your SQL files.

  pgmi metadata scaffold ./project --write
  pgmi metadata validate ./project
  pgmi metadata plan ./project --json

All three subcommands operate purely on the filesystem — no database
connection is opened.`,
}

var metadataScaffoldCmd = &cobra.Command{
	Use:   "scaffold <project_path>",
	Short: "Add <pgmi-meta> blocks to files that lack one",
	Long: `Add <pgmi-meta> blocks to SQL files that don't have one. Each new block
gets a fallback UUID derived from the file path and the default settings.

  pgmi metadata scaffold ./project              Preview only
  pgmi metadata scaffold ./project --write      Apply to files
  pgmi metadata scaffold ./project --idempotent=false --write

Without --write, no files are modified.`,
	Args:              RequireProjectPath,
	ValidArgsFunction: completeDirectories,
	RunE:              runMetadataScaffold,
}

var metadataValidateCmd = &cobra.Command{
	Use:   "validate <project_path>",
	Short: "Check <pgmi-meta> XML validity and uniqueness",
	Long: `Check that every <pgmi-meta> block parses, conforms to the XSD schema,
and that no two files share an id.

  pgmi metadata validate ./project
  pgmi metadata validate ./project --json

Exit non-zero on any failure.`,
	Args:              RequireProjectPath,
	ValidArgsFunction: completeDirectories,
	RunE:              runMetadataValidate,
}

var metadataPlanCmd = &cobra.Command{
	Use:   "plan <project_path>",
	Short: "Show files in execution order from sortKeys",
	Long: `Show every SQL file with its id, sortKeys, and idempotent flag, ordered
the way pgmi_plan_view would order them at deploy time.

  pgmi metadata plan ./project
  pgmi metadata plan ./project --json

Use this to verify ordering before running ` + "`pgmi deploy`" + `.`,
	Args:              RequireProjectPath,
	ValidArgsFunction: completeDirectories,
	RunE:              runMetadataPlan,
}

var (
	// Scaffold flags
	scaffoldIdempotent bool
	scaffoldWrite      bool

	// Validate flags
	validateJSON bool

	// Plan flags
	planJSON bool
)

func init() {
	rootCmd.AddCommand(metadataCmd)
	metadataCmd.AddCommand(metadataScaffoldCmd)
	metadataCmd.AddCommand(metadataValidateCmd)
	metadataCmd.AddCommand(metadataPlanCmd)

	// Scaffold flags
	metadataScaffoldCmd.Flags().BoolVar(&scaffoldWrite, "write", false, "Write generated metadata to files (default: preview only)")
	metadataScaffoldCmd.Flags().BoolVar(&scaffoldIdempotent, "idempotent", true, "Mark generated scripts as idempotent (default: true)")

	// Validate flags
	metadataValidateCmd.Flags().BoolVar(&validateJSON, "json", false, "Output validation results as JSON")

	// Plan flags
	metadataPlanCmd.Flags().BoolVar(&planJSON, "json", false, "Output execution plan as JSON")
}

// runMetadataScaffold generates metadata for files without metadata
func runMetadataScaffold(cmd *cobra.Command, args []string) error {
	projectPath := args[0]
	verbose := getVerboseFlag(cmd)

	// Preview mode unless --write is specified
	previewOnly := !scaffoldWrite

	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Project path: %s\n", projectPath)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Preview only: %v\n", previewOnly)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Default idempotent: %v\n", scaffoldIdempotent)
	}

	// Create scanner
	fileScanner := scanner.NewScanner(checksum.New())

	// Scan directory
	fmt.Fprintln(os.Stderr, "Scanning SQL files...")
	scanResult, err := fileScanner.ScanDirectory(projectPath)
	if err != nil {
		return fmt.Errorf("failed to scan directory: %w", err)
	}

	// Filter files without metadata
	var filesWithoutMetadata []string
	for _, file := range scanResult.Files {
		if file.Metadata == nil {
			filesWithoutMetadata = append(filesWithoutMetadata, file.Path)
		}
	}

	if len(filesWithoutMetadata) == 0 {
		fmt.Fprintln(os.Stderr, "All SQL files already have metadata.")
		return nil
	}

	fmt.Fprintf(os.Stderr, "Found %d file(s) without metadata:\n\n", len(filesWithoutMetadata))

	// Generate metadata for each file
	for _, filePath := range filesWithoutMetadata {
		relPath := filePath
		if !filepath.IsAbs(filePath) {
			relPath = "./" + strings.ReplaceAll(filePath, "\\", "/")
		}

		// Generate fallback UUID
		fallbackID := metadata.GenerateFallbackID(relPath)

		// Build metadata block
		metaBlock := fmt.Sprintf(`/*
<pgmi-meta
    id="%s"
    idempotent="%v">
  <description>
    Auto-generated metadata for %s
  </description>
  <sortKeys>
    <key>generated/%s</key>
  </sortKeys>
</pgmi-meta>
*/

`, fallbackID, scaffoldIdempotent, filepath.Base(filePath), filepath.Base(filePath))

		fmt.Fprintf(os.Stderr, "  %s\n", filePath)
		fmt.Fprintf(os.Stderr, "    ID: %s (fallback)\n", fallbackID)
		fmt.Fprintf(os.Stderr, "    Idempotent: %v\n", scaffoldIdempotent)

		if !previewOnly {
			// Read existing file content
			absPath := filePath
			if !filepath.IsAbs(filePath) {
				absPath = filepath.Join(projectPath, filePath)
			}
			content, err := os.ReadFile(absPath)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %w", filePath, err)
			}

			// Prepend metadata block
			newContent := metaBlock + string(content)

			// Atomic write: write to a sibling .tmp then os.Rename. A crash
			// between Write and Rename leaves the original intact; a crash
			// during Rename is handled atomically by the OS. Preserves the
			// source file's mode so we don't silently widen permissions.
			origInfo, err := os.Stat(absPath)
			if err != nil {
				return fmt.Errorf("failed to stat file %s: %w", filePath, err)
			}
			tmpPath := absPath + ".pgmi-tmp"
			if err := os.WriteFile(tmpPath, []byte(newContent), origInfo.Mode().Perm()); err != nil {
				return fmt.Errorf("failed to write %s: %w", filePath, err)
			}
			if err := os.Rename(tmpPath, absPath); err != nil {
				_ = os.Remove(tmpPath)
				return fmt.Errorf("failed to finalise write of %s: %w", filePath, err)
			}

			fmt.Fprintf(os.Stderr, "    Written to file\n")
		}
		fmt.Fprintln(os.Stderr)
	}

	if previewOnly {
		fmt.Fprintln(os.Stderr, "Preview mode: no files were modified. Use --write to apply changes.")
	} else {
		fmt.Fprintf(os.Stderr, "Generated metadata for %d file(s).\n", len(filesWithoutMetadata))
	}

	return nil
}

// runMetadataValidate validates metadata across all files
func runMetadataValidate(cmd *cobra.Command, args []string) error {
	projectPath := args[0]
	verbose := getVerboseFlag(cmd)

	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Project path: %s\n", projectPath)
		fmt.Fprintf(os.Stderr, "[VERBOSE] JSON output: %v\n", validateJSON)
	}

	if !validateJSON {
		fmt.Fprintln(os.Stderr, "Scanning and validating SQL files...")
		fmt.Fprintln(os.Stderr, "Validating metadata graph...")
	}

	result, err := validateProject(projectPath)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	if validateJSON {
		jsonBytes, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(jsonBytes))
	} else {
		fmt.Fprintf(os.Stderr, "\nValidation Summary:\n")
		fmt.Fprintf(os.Stderr, "  Total files: %d\n", result.TotalFiles)
		fmt.Fprintf(os.Stderr, "  Files with metadata: %d\n", result.FilesWithMetadata)
		fmt.Fprintf(os.Stderr, "  Files without metadata: %d\n", result.FilesWithoutMetadata)
		fmt.Fprintln(os.Stderr)

		if len(result.DuplicateIDs) > 0 {
			fmt.Fprintln(os.Stderr, "Error: Duplicate IDs detected:")
			for _, dup := range result.DuplicateIDs {
				fmt.Fprintln(os.Stderr, "  "+dup)
			}
			fmt.Fprintln(os.Stderr)
		}

		if result.ValidationPassed {
			fmt.Fprintln(os.Stderr, "Metadata validation passed.")
		}
	}

	if !result.ValidationPassed {
		return fmt.Errorf("metadata validation failed")
	}

	return nil
}

// runMetadataPlan shows execution plan based on metadata
func runMetadataPlan(cmd *cobra.Command, args []string) error {
	projectPath := args[0]
	verbose := getVerboseFlag(cmd)

	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Project path: %s\n", projectPath)
		fmt.Fprintf(os.Stderr, "[VERBOSE] JSON output: %v\n", planJSON)
	}

	if !planJSON {
		fmt.Fprintln(os.Stderr, "Scanning SQL files and analyzing dependencies...")
	}

	result, err := planProject(projectPath)
	if err != nil {
		return fmt.Errorf("failed to scan directory: %w", err)
	}
	plan := result.Plan

	// Output plan
	if planJSON {
		jsonBytes, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(jsonBytes))
	} else {
		// Human-readable output
		fmt.Fprintf(os.Stderr, "\nMetadata Summary (%d files):\n\n", len(plan))

		for i, entry := range plan {
			fmt.Fprintf(os.Stderr, "%d. %s\n", i+1, entry.Path)
			fmt.Fprintf(os.Stderr, "   ID: %s\n", entry.ID)
			if entry.Description != "" {
				fmt.Fprintf(os.Stderr, "   Description: %s\n", entry.Description)
			}
			fmt.Fprintf(os.Stderr, "   Idempotent: %v\n", entry.Idempotent)
			fmt.Fprintf(os.Stderr, "   Sort Keys: %v\n", entry.SortKeys)
			fmt.Fprintln(os.Stderr)
		}

		fmt.Fprintln(os.Stderr, "Note: Actual execution order is determined by sort keys during deployment.")
	}

	return nil
}
