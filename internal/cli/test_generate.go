package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/services/testgen"
	"github.com/vvka-141/pgmi/internal/testdiscovery"
	basetestgen "github.com/vvka-141/pgmi/internal/testgen"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var testGenerateCmd = &cobra.Command{
	Use:   "generate <project_path>",
	Short: "Generate standalone SQL test script",
	Long: `Generate a standalone SQL test script that can run without pgmi.

This command:
1. Scans the project for test files (/__test__/ or /__tests__/ directories)
2. Builds the test execution plan with fixtures and teardowns
3. Generates a self-contained SQL script with embedded test content

The generated script can be executed directly with psql:
  psql -f tests.sql

By default, runs in dry-run mode (previews the script without writing).

Examples:
  # Preview generated script (dry-run)
  pgmi test generate ./myapp

  # Generate script to file
  pgmi test generate ./myapp -o tests.sql

  # Generate to stdout (for piping)
  pgmi test generate ./myapp -o -

  # Filter tests by pattern
  pgmi test generate ./myapp --filter auth -o auth_tests.sql

  # Generate without transaction wrapper
  pgmi test generate ./myapp --with-transaction=false -o tests.sql

  # Generate with debug output for savepoints
  pgmi test generate ./myapp --with-debug -o tests.sql`,
	Args: cobra.ExactArgs(1),
	RunE: runTestGenerate,
}

type testGenerateFlagValues struct {
	output          string
	filter          string
	withTransaction bool
	withNotices     bool
	withDebug       bool
	callback        string
}

var testGenerateFlags testGenerateFlagValues

func init() {
	testCmd.AddCommand(testGenerateCmd)

	testGenerateCmd.Flags().StringVarP(&testGenerateFlags.output, "output", "o", "",
		"Output file path, or '-' for stdout. If not specified, runs in dry-run mode (preview only)")
	testGenerateCmd.Flags().StringVarP(&testGenerateFlags.filter, "filter", "f", ".*",
		"POSIX regex pattern to filter tests (default: \".*\" matches all)")
	testGenerateCmd.Flags().BoolVar(&testGenerateFlags.withTransaction, "with-transaction", true,
		"Wrap generated script in BEGIN/ROLLBACK")
	testGenerateCmd.Flags().BoolVar(&testGenerateFlags.withNotices, "with-notices", true,
		"Include RAISE NOTICE for progress output")
	testGenerateCmd.Flags().BoolVar(&testGenerateFlags.withDebug, "with-debug", false,
		"Include RAISE DEBUG for savepoint operations")
	testGenerateCmd.Flags().StringVar(&testGenerateFlags.callback, "callback", "",
		"Custom callback function for test events (e.g., pg_temp.my_logger)\n"+
			"When specified, includes the pgmi_test_event type and callback stub in output")
}

func runTestGenerate(cmd *cobra.Command, args []string) error {
	sourcePath := args[0]
	verbose := getVerboseFlag(cmd)

	// Validate callback format early
	if err := basetestgen.ValidateCallbackName(testGenerateFlags.callback); err != nil {
		return err
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Source path: %s\n", sourcePath)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Filter pattern: %s\n", testGenerateFlags.filter)
		fmt.Fprintf(os.Stderr, "[VERBOSE] Output: %s\n", testGenerateFlags.output)
	}

	// Scan project files
	fileScanner := scanner.NewScanner(checksum.New())
	scanResult, err := fileScanner.ScanDirectory(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to scan directory: %w", err)
	}

	// Build test tree from files
	files := make([]pgmi.FileMetadata, len(scanResult.Files))
	for i, f := range scanResult.Files {
		files[i] = pgmi.FileMetadata{
			Path:      f.Path,
			Directory: f.Directory,
			Name:      f.Name,
			Content:   f.Content,
		}
	}

	tree, resolver := buildTestTreeAndResolver(files)
	if testGenerateFlags.filter != "" && testGenerateFlags.filter != ".*" {
		tree = tree.FilterByPattern(testGenerateFlags.filter)
	}

	// Build test plan
	planBuilder := testdiscovery.NewPlanBuilder(resolver)
	steps, err := planBuilder.Build(tree)
	if err != nil {
		return fmt.Errorf("failed to build test plan: %w", err)
	}

	if len(steps) == 0 {
		fmt.Fprintln(os.Stderr, "No tests found matching filter pattern")
		return nil
	}

	// Generate script
	config := testgen.Config{
		WithTransaction: testGenerateFlags.withTransaction,
		WithNotices:     testGenerateFlags.withNotices,
		WithDebug:       testGenerateFlags.withDebug,
		Callback:        testGenerateFlags.callback,
	}
	generator := testgen.New(config)
	result := generator.Generate(steps, sourcePath, testGenerateFlags.filter)

	// Output handling
	if testGenerateFlags.output == "" {
		// Dry-run mode: preview to stderr
		fmt.Fprintln(os.Stderr, "=== Generated Test Script (dry-run preview) ===")
		fmt.Fprintln(os.Stderr)
		fmt.Fprint(os.Stderr, result.Script)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "Summary: %d fixture(s), %d test(s), %d teardown(s)\n",
			result.FixtureCount, result.TestCount, result.TeardownCount)
		fmt.Fprintln(os.Stderr, "Use -o <file> to write to file, or -o - for stdout")
		return nil
	}

	if testGenerateFlags.output == "-" {
		// Write to stdout
		fmt.Print(result.Script)
	} else {
		// Write to file
		if err := os.WriteFile(testGenerateFlags.output, []byte(result.Script), 0644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Generated %s (%d fixture(s), %d test(s), %d teardown(s))\n",
			testGenerateFlags.output, result.FixtureCount, result.TestCount, result.TeardownCount)
	}

	return nil
}

// buildTestTreeAndResolver creates a test tree and content resolver from files.
func buildTestTreeAndResolver(files []pgmi.FileMetadata) (*testdiscovery.TestTree, testdiscovery.ContentResolver) {
	contentMap := make(map[string]string)
	for _, f := range files {
		if pgmi.IsTestPath(f.Path) {
			contentMap[f.Path] = f.Content
		}
	}

	resolver := func(path string) (string, error) {
		if content, ok := contentMap[path]; ok {
			return content, nil
		}
		return "", fmt.Errorf("test file not found: %s", path)
	}

	sources := testdiscovery.ConvertFromFileMetadata(files)
	discoverer := testdiscovery.NewDiscoverer(nil)
	tree, _ := discoverer.Discover(sources)

	return tree, resolver
}
