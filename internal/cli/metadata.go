package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/pkg/pgmi"
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
	Args: usageArgs(cobra.NoArgs),
	RunE: showHelp,
}

var metadataScaffoldCmd = &cobra.Command{
	Use:   "scaffold <project_path>",
	Short: "Add <pgmi-meta> blocks to files that lack one",
	Long: `Add <pgmi-meta> blocks to SQL files that don't have one. Each new block
gets a random id and the sort key <prefix>/<file name>.

The prefix places the file in the execution order, so it comes from you:
--sort-key, or else the prefix the files with metadata in the same directory
already share. A directory path cannot supply it: layers do not run in
alphabetical order (core before api), and an advanced project's keys belong
in its numbered bands.

  pgmi metadata scaffold ./project/core --sort-key 005/core/          Preview only
  pgmi metadata scaffold ./project/core --sort-key 005/core/ --write  Apply to files
  pgmi metadata scaffold ./project --write      Prefixes from existing metadata

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
	Short: "Show the rows pgmi_plan_view will hold, in order",
	Long: `Show, without a database, the rows pgmi_plan_view will hold at deploy time:
every loaded non-test file once per sort key (its path when it has none), in
execution order. Non-SQL files are listed and marked, as the view includes them.

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
	scaffoldSortKey    string
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
	metadataScaffoldCmd.Flags().StringVar(&scaffoldSortKey, "sort-key", "", "Sort-key prefix for the generated keys, e.g. 005/core/ (default: the prefix of existing metadata in the same directory)")

	// Validate flags
	metadataValidateCmd.Flags().BoolVar(&validateJSON, "json", false, "Output validation results as JSON")

	// Plan flags
	metadataPlanCmd.Flags().BoolVar(&planJSON, "json", false, "Output execution plan as JSON")
}

// runMetadataScaffold generates metadata for files without metadata
func runMetadataScaffold(cmd *cobra.Command, args []string) error {
	projectPath := args[0]
	verbose := getVerboseFlag(cmd)
	previewOnly := !scaffoldWrite

	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Project path: %s\n", projectPath)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Preview only: %v\n", previewOnly)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Default idempotent: %v\n", scaffoldIdempotent)
	}

	fmt.Fprintln(os.Stderr, "Scanning SQL files...")
	scanResult, err := scanner.NewScanner(checksum.New()).ScanDirectory(projectPath)
	if err != nil {
		return fmt.Errorf("failed to scan directory: %w", err)
	}

	var pending []string
	for _, file := range scanResult.Files {
		if file.Metadata == nil && pgmi.IsSQLExtension(file.Extension) && !pgmi.IsTestPath(file.Path) {
			pending = append(pending, file.Path)
		}
	}
	if len(pending) == 0 {
		fmt.Fprintln(os.Stderr, "All SQL files already have metadata.")
		return nil
	}

	keys, err := scaffoldSortKeys(scanResult.Files, pending, scaffoldSortKey)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Found %d file(s) without metadata:\n\n", len(pending))
	for _, filePath := range pending {
		id := uuid.New()
		metaBlock := fmt.Sprintf(`/*
<pgmi-meta
    id="%s"
    idempotent="%v">
  <description>
    %s
  </description>
  <sortKeys>
    <key>%s</key>
  </sortKeys>
</pgmi-meta>
*/

`, id, scaffoldIdempotent, path.Base(filePath), keys[filePath])

		fmt.Fprintf(os.Stderr, "  %s\n", filePath)
		fmt.Fprintf(os.Stderr, "    ID: %s\n", id)
		fmt.Fprintf(os.Stderr, "    Sort key: %s\n", keys[filePath])
		fmt.Fprintf(os.Stderr, "    Idempotent: %v\n", scaffoldIdempotent)

		if !previewOnly {
			if err := prependToFile(filepath.Join(projectPath, filepath.FromSlash(filePath)), metaBlock); err != nil {
				return fmt.Errorf("failed to write %s: %w", filePath, err)
			}
			fmt.Fprintf(os.Stderr, "    Written to file\n")
		}
		fmt.Fprintln(os.Stderr)
	}

	if previewOnly {
		fmt.Fprintln(os.Stderr, "Preview mode: no files were modified. Use --write to apply changes.")
	} else {
		fmt.Fprintf(os.Stderr, "Generated metadata for %d file(s).\n", len(pending))
	}
	return nil
}

// scaffoldSortKeys gives each pending file the key <prefix>/<file name>. The
// prefix is the caller's --sort-key, or else the one every sibling with
// metadata in the same directory already uses. A path cannot supply it: the
// order between layers (core before api) is not alphabetical, and an advanced
// project's keys must land in its numbered bands.
func scaffoldSortKeys(files []pgmi.FileMetadata, pending []string, flagPrefix string) (map[string]string, error) {
	prefixes := map[string]map[string]bool{}
	for _, f := range files {
		if f.Metadata == nil || len(f.Metadata.SortKeys) == 0 {
			continue
		}
		dir := path.Dir(f.Path)
		if prefixes[dir] == nil {
			prefixes[dir] = map[string]bool{}
		}
		key := f.Metadata.SortKeys[0]
		prefixes[dir][key[:strings.LastIndex(key, "/")+1]] = true
	}

	keys := make(map[string]string, len(pending))
	var unresolved []string
	for _, p := range pending {
		prefix := ""
		if flagPrefix != "" {
			prefix = strings.TrimSuffix(flagPrefix, "/") + "/"
		} else if siblings := prefixes[path.Dir(p)]; len(siblings) == 1 {
			for s := range siblings {
				prefix = s
			}
		}
		if prefix == "" {
			unresolved = append(unresolved, p)
			continue
		}
		keys[p] = prefix + path.Base(p)
	}
	if len(unresolved) > 0 {
		return nil, fmt.Errorf("no sort-key prefix for %s: no file with metadata in the same directory gives one; "+
			"pass --sort-key with the layer, e.g. --sort-key 005/core/: %w",
			strings.Join(unresolved, ", "), pgmi.ErrUsage)
	}
	return keys, nil
}

// prependToFile writes block ahead of the file's content through a sibling
// temp file and a rename, so a crash leaves the original intact, and keeps
// the file's mode.
func prependToFile(absPath, block string) error {
	content, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}
	tmpPath := absPath + ".pgmi-tmp"
	if err := os.WriteFile(tmpPath, []byte(block+string(content)), info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// runMetadataValidate validates metadata across all files
func runMetadataValidate(cmd *cobra.Command, args []string) error {
	projectPath := args[0]
	verbose := getVerboseFlag(cmd)
	out := cmd.OutOrStdout()

	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Project path: %s\n", projectPath)
		fmt.Fprintf(os.Stderr, "[VERBOSE] JSON output: %v\n", validateJSON)
	}

	result, err := validateProject(projectPath)
	if err != nil {
		err = fmt.Errorf("validation failed: %w", err)
		if validateJSON {
			printMetadataJSONFailure(out, map[string]any{"validationPassed": false}, err)
		}
		return err
	}

	var failure error
	if !result.ValidationPassed {
		failure = fmt.Errorf("metadata validation failed: %d duplicate id(s): %w", len(result.DuplicateIDs), pgmi.ErrInvalidConfig)
		result.Error, result.ExitCode = failure.Error(), pgmi.ExitCodeForError(failure)
	}

	if validateJSON {
		if err := printJSON(out, result); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "Total files: %d\n", result.TotalFiles)
		fmt.Fprintf(out, "Files with metadata: %d\n", result.FilesWithMetadata)
		fmt.Fprintf(out, "Files without metadata: %d\n", result.FilesWithoutMetadata)
		for _, dup := range result.DuplicateIDs {
			fmt.Fprintln(out, "Duplicate id: "+dup)
		}
		for _, w := range result.Warnings {
			fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+w)
		}
	}
	return failure
}

// runMetadataPlan prints the rows pgmi_plan_view will hold, in execution order.
// The listing is the command's output, so it goes to stdout.
func runMetadataPlan(cmd *cobra.Command, args []string) error {
	projectPath := args[0]
	verbose := getVerboseFlag(cmd)
	out := cmd.OutOrStdout()

	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Project path: %s\n", projectPath)
		fmt.Fprintf(os.Stderr, "[VERBOSE] JSON output: %v\n", planJSON)
	}

	result, err := planProject(projectPath)
	if err != nil {
		if planJSON {
			printMetadataJSONFailure(out, map[string]any{}, err)
		}
		return err
	}

	if planJSON {
		return printJSON(out, result)
	}
	for _, e := range result.Plan {
		note := ""
		if !e.IsSQLFile {
			note = "  (not SQL)"
		}
		fmt.Fprintf(out, "%4d  %-28s %s%s\n", e.ExecutionOrder, e.SortKey, e.Path, note)
	}
	return nil
}

func printJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	fmt.Fprintln(w, string(b))
	return nil
}

// printMetadataJSONFailure keeps --json a contract on failure too, as deploy
// --json does: a caller parsing stdout gets an envelope, not nothing.
func printMetadataJSONFailure(w io.Writer, fields map[string]any, err error) {
	fields["error"] = err.Error()
	fields["exitCode"] = pgmi.ExitCodeForError(err)
	_ = printJSON(w, fields)
}
