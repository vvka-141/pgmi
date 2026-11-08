package cli

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/pathrule"
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
	metadataScaffoldCmd.Flags().BoolVar(&scaffoldIdempotent, "idempotent", true, "Mark generated scripts as idempotent")
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

	root, under, err := scaffoldScope(projectPath)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Scanning SQL files...")
	scanResult, err := scanner.NewScanner(checksum.New()).ScanDirectory(root)
	if err != nil {
		return fmt.Errorf("failed to scan directory: %w", err)
	}

	var pending []string
	for _, file := range scanResult.Files {
		if file.Metadata == nil && pathrule.IsSQLExtension(file.Extension) && !pathrule.IsTestPath(file.Path) &&
			strings.HasPrefix(file.Path, under) {
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
		sortKeys, shownKey := "", "(none, path order)"
		if key := keys[filePath]; key != "" {
			sortKeys = fmt.Sprintf("  <sortKeys>\n    <key>%s</key>\n  </sortKeys>\n", xmlText(key))
			shownKey = key
		}
		metaBlock := fmt.Sprintf(`/*
<pgmi-meta
    id="%s"
    idempotent="%v">
  <description>
    %s
  </description>
%s</pgmi-meta>
*/

`, id, scaffoldIdempotent, xmlText(path.Base(filePath)), sortKeys)

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "  %s\n", filePath)
		fmt.Fprintf(out, "    ID: %s\n", id)
		fmt.Fprintf(out, "    Sort key: %s\n", shownKey)
		fmt.Fprintf(out, "    Idempotent: %v\n", scaffoldIdempotent)

		if !previewOnly {
			target := filepath.Join(root, filepath.FromSlash(filePath))
			if err := prependToFile(target, metaBlock); err != nil {
				return fmt.Errorf("failed to write %s: %w", filePath, err)
			}
			fmt.Fprintf(os.Stderr, "    Written to %s\n", target)
		}
		fmt.Fprintln(out)
	}

	if previewOnly {
		fmt.Fprintln(os.Stderr, "Preview mode: no files were modified. Use --write to apply changes.")
	} else {
		fmt.Fprintf(os.Stderr, "Generated metadata for %d file(s).\n", len(pending))
	}
	return nil
}

// scaffoldScope finds the project a directory belongs to: the nearest
// directory at or above it holding deploy.sql. Scanning from there gives the
// paths, sort keys and .pgmiignore that pgmi metadata scaffold . would; under
// is the scanned-path prefix of the files to scaffold. Without a deploy.sql
// above it, the directory is its own root.
func scaffoldScope(dir string) (root, under string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "deploy.sql")); err == nil {
			rel, err := filepath.Rel(d, abs)
			if err != nil {
				return "", "", err
			}
			if rel == "." {
				return d, "./", nil
			}
			return d, "./" + filepath.ToSlash(rel) + "/", nil
		}
		if filepath.Dir(d) == d {
			return abs, "./", nil
		}
	}
}

// scaffoldSortKeys gives each pending file the key <prefix>/<file name>. The
// prefix is the caller's --sort-key, or else the one every sibling with
// metadata in the same directory already uses. A path cannot supply it: the
// order between layers (core before api) is not alphabetical, and an advanced
// project's keys must land in its numbered bands. A directory whose metadata
// gives no sortKeys at all (the basic template) runs in path order, so its
// new files get no key either: an empty string.
func scaffoldSortKeys(files []pgmi.FileMetadata, pending []string, flagPrefix string) (map[string]string, error) {
	prefixes := map[string]map[string]bool{}
	withMetadata := map[string]bool{}
	for _, f := range files {
		if f.Metadata == nil {
			continue
		}
		dir := path.Dir(f.Path)
		withMetadata[dir] = true
		if len(f.Metadata.SortKeys) == 0 {
			continue
		}
		if prefixes[dir] == nil {
			prefixes[dir] = map[string]bool{}
		}
		key := f.Metadata.SortKeys[0]
		prefixes[dir][key[:strings.LastIndex(key, "/")+1]] = true
	}

	keys := make(map[string]string, len(pending))
	var noMetadata, disagree []string
	for _, p := range pending {
		dir := path.Dir(p)
		siblings := prefixes[dir]
		switch {
		case flagPrefix != "":
			keys[p] = strings.TrimSuffix(flagPrefix, "/") + "/" + path.Base(p)
		case len(siblings) == 1:
			for s := range siblings {
				keys[p] = s + path.Base(p)
			}
		case len(siblings) > 1:
			disagree = append(disagree, p)
		case withMetadata[dir]:
			keys[p] = ""
		default:
			noMetadata = append(noMetadata, p)
		}
	}
	var reasons []string
	if len(noMetadata) > 0 {
		reasons = append(reasons, "no file in the same directory has metadata: "+strings.Join(noMetadata, ", "))
	}
	if len(disagree) > 0 {
		reasons = append(reasons, "the files in the same directory use different sort-key prefixes: "+strings.Join(disagree, ", "))
	}
	if len(reasons) > 0 {
		example := append(noMetadata, disagree...)[0]
		return nil, fmt.Errorf("no sort-key prefix to derive; %s\n"+
			"pass --sort-key with the layer, e.g. --sort-key 005/core/ (advanced), "+
			"or --sort-key ./%s to keep path order: %w",
			strings.Join(reasons, "; "), strings.TrimPrefix(path.Dir(example)+"/", "./"), pgmi.ErrUsage)
	}
	return keys, nil
}

// xmlText is s escaped for XML character data.
func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
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
	// A BOM stays first, where the scanner strips it; behind the block it
	// would reach PostgreSQL as part of the first statement.
	bom, rest := "", string(content)
	if strings.HasPrefix(rest, "\ufeff") {
		bom, rest = "\ufeff", strings.TrimPrefix(rest, "\ufeff")
	}
	tmpPath := absPath + ".pgmi-tmp"
	if err := os.WriteFile(tmpPath, []byte(bom+block+rest), info.Mode().Perm()); err != nil {
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
		fields := map[string]any{"validationPassed": false}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			var each []string
			for _, e := range joined.Unwrap() {
				each = append(each, e.Error())
			}
			fields["errors"] = each
		}
		err = fmt.Errorf("validation failed: %w", err)
		if validateJSON {
			printMetadataJSONFailure(out, fields, err)
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
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ORDER\tSORT KEY\tPATH\tIDEMPOTENT")
	for _, e := range result.Plan {
		note := ""
		if !e.IsSQLFile {
			note = "  (not SQL)"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%v%s\n", e.ExecutionOrder, e.SortKey, e.Path, e.Idempotent, note)
	}
	return tw.Flush()
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
