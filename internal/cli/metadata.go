package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/metadata"
	"github.com/spf13/cobra"
)

var metadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "Metadata operations for SQL files",
	Long: `Metadata commands for managing SQL file metadata.

Available commands:
  scaffold  Generate metadata for SQL files without metadata
  validate  Validate metadata in SQL files (no DB connection required)
  plan      Show execution plan based on metadata (no DB connection required)

Examples:
  # Generate metadata for all files without metadata
  pgmi metadata scaffold ./myproject

  # Validate metadata across all files
  pgmi metadata validate ./myproject

  # Show execution plan as JSON
  pgmi metadata plan ./myproject --json`,
}

// Scaffold command
var metadataScaffoldCmd = &cobra.Command{
	Use:   "scaffold <project_path>",
	Short: "Generate metadata for SQL files without metadata",
	Long: `Generate metadata blocks for SQL files that don't have metadata.

This command:
1. Scans the project directory for SQL files
2. Identifies files without <pgmi-meta> blocks
3. Generates metadata with fallback UUIDs and default settings
4. Optionally writes metadata to files (with --write flag)

By default, previews changes without modifying files.

Examples:
  # Preview metadata generation
  pgmi metadata scaffold ./myproject

  # Write metadata to files
  pgmi metadata scaffold ./myproject --write

  # Customize generated metadata
  pgmi metadata scaffold ./myproject --idempotent=true --write`,
	Args: cobra.ExactArgs(1),
	RunE: runMetadataScaffold,
}

// Validate command
var metadataValidateCmd = &cobra.Command{
	Use:   "validate <project_path>",
	Short: "Validate metadata in SQL files",
	Long: `Validate metadata blocks in SQL files without connecting to database.

This command checks:
1. XML syntax and namespace correctness
2. XSD schema compliance (required fields, UUID format, etc.)
3. Duplicate IDs across files

Examples:
  # Validate all metadata
  pgmi metadata validate ./myproject

  # Validate with verbose output
  pgmi metadata validate ./myproject --verbose

  # Validate with JSON output
  pgmi metadata validate ./myproject --json`,
	Args: cobra.ExactArgs(1),
	RunE: runMetadataValidate,
}

// Plan command
var metadataPlanCmd = &cobra.Command{
	Use:   "plan <project_path>",
	Short: "Show execution plan based on metadata",
	Long: `Display execution plan derived from metadata sort keys.

This command:
1. Scans SQL files and extracts metadata
2. Shows files with their IDs and sort keys
3. Displays metadata information for each file

No database connection required - analysis is purely metadata-driven.

Examples:
  # Show execution plan (human-readable)
  pgmi metadata plan ./myproject

  # Show execution plan as JSON
  pgmi metadata plan ./myproject --json

  # Show plan with verbose details
  pgmi metadata plan ./myproject --verbose`,
	Args: cobra.ExactArgs(1),
	RunE: runMetadataPlan,
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
		fmt.Fprintln(os.Stderr, "✓ All SQL files already have metadata")
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

			// Write back to file
			if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
				return fmt.Errorf("failed to write file %s: %w", filePath, err)
			}

			fmt.Fprintf(os.Stderr, "    ✓ Written to file\n")
		}
		fmt.Fprintln(os.Stderr)
	}

	if previewOnly {
		fmt.Fprintln(os.Stderr, "ℹ Preview mode: No files were modified")
		fmt.Fprintln(os.Stderr, "  Use --write flag to apply changes")
	} else {
		fmt.Fprintf(os.Stderr, "✓ Generated metadata for %d file(s)\n", len(filesWithoutMetadata))
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

	// Create scanner
	fileScanner := scanner.NewScanner(checksum.New())

	// Scan directory (this performs metadata extraction and validation)
	if !validateJSON {
		fmt.Fprintln(os.Stderr, "Scanning and validating SQL files...")
	}

	scanResult, err := fileScanner.ScanDirectory(projectPath)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Perform cross-file validation
	if !validateJSON {
		fmt.Fprintln(os.Stderr, "Validating metadata graph...")
	}

	// Check for duplicate IDs
	idToPath := make(map[uuid.UUID]string)
	var duplicates []string
	for _, file := range scanResult.Files {
		if file.Metadata == nil {
			continue
		}
		if existingPath, exists := idToPath[file.Metadata.ID]; exists {
			duplicates = append(duplicates, fmt.Sprintf("  Duplicate ID %s:\n    - %s\n    - %s", file.Metadata.ID, existingPath, file.Path))
		} else {
			idToPath[file.Metadata.ID] = file.Path
		}
	}

	// Cross-file validation complete (duplicate IDs checked above)

	// Collect results
	filesWithMetadata := 0
	filesWithoutMetadata := 0
	for _, file := range scanResult.Files {
		if file.Metadata != nil {
			filesWithMetadata++
		} else {
			filesWithoutMetadata++
		}
	}

	validationPassed := len(duplicates) == 0

	// Output results
	if validateJSON {
		result := map[string]interface{}{
			"total_files":            len(scanResult.Files),
			"files_with_metadata":    filesWithMetadata,
			"files_without_metadata": filesWithoutMetadata,
			"validation_passed":      validationPassed,
			"duplicate_ids":          duplicates,
		}
		jsonBytes, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(jsonBytes))
	} else {
		// Human-readable output
		fmt.Fprintf(os.Stderr, "\nValidation Summary:\n")
		fmt.Fprintf(os.Stderr, "  Total files: %d\n", len(scanResult.Files))
		fmt.Fprintf(os.Stderr, "  Files with metadata: %d\n", filesWithMetadata)
		fmt.Fprintf(os.Stderr, "  Files without metadata: %d\n", filesWithoutMetadata)
		fmt.Fprintln(os.Stderr)

		if len(duplicates) > 0 {
			fmt.Fprintln(os.Stderr, "✗ Duplicate IDs detected:")
			for _, dup := range duplicates {
				fmt.Fprintln(os.Stderr, dup)
			}
			fmt.Fprintln(os.Stderr)
		}

		if validationPassed {
			fmt.Fprintln(os.Stderr, "✓ Metadata validation passed")
		} else {
			return fmt.Errorf("metadata validation failed")
		}
	}

	if !validationPassed {
		return fmt.Errorf("validation failed with errors")
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

	// Create scanner
	fileScanner := scanner.NewScanner(checksum.New())

	// Scan directory
	if !planJSON {
		fmt.Fprintln(os.Stderr, "Scanning SQL files and analyzing dependencies...")
	}

	scanResult, err := fileScanner.ScanDirectory(projectPath)
	if err != nil {
		return fmt.Errorf("failed to scan directory: %w", err)
	}

	// Build plan showing files with their metadata

	type PlanEntry struct {
		Path        string   `json:"path"`
		ID          string   `json:"id"`
		Idempotent  bool     `json:"idempotent"`
		SortKeys    []string `json:"sort_keys"`
		Description string   `json:"description"`
	}

	var plan []PlanEntry
	for _, file := range scanResult.Files {
		if file.Metadata == nil {
			// Use fallback
			fallbackID := metadata.GenerateFallbackID(file.Path)
			plan = append(plan, PlanEntry{
				Path:        file.Path,
				ID:          fallbackID.String(),
				Idempotent:  true,
				SortKeys:    []string{filepath.Base(file.Path)},
				Description: "No metadata (fallback)",
			})
		} else {
			plan = append(plan, PlanEntry{
				Path:        file.Path,
				ID:          file.Metadata.ID.String(),
				Idempotent:  file.Metadata.Idempotent,
				SortKeys:    file.Metadata.SortKeys,
				Description: file.Metadata.Description,
			})
		}
	}

	// Output plan
	if planJSON {
		result := map[string]interface{}{
			"total_files": len(plan),
			"plan":        plan,
		}
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

		fmt.Fprintln(os.Stderr, "ℹ Actual execution order is determined by sort keys during deployment")
	}

	return nil
}
