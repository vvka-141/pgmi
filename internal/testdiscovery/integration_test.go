package testdiscovery

import (
	"fmt"
	"path"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/files/filesystem"
)

func sourcesFromMemFS(fs *filesystem.MemoryFileSystem, root string) ([]Source, error) {
	dir, err := fs.Open(root)
	if err != nil {
		return nil, err
	}

	var sources []Source
	err = dir.Walk(func(file filesystem.File, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if file.Info().IsDir() {
			return nil
		}

		relPath := file.RelativePath()
		directory := path.Dir(relPath)
		if !strings.HasSuffix(directory, "/") {
			directory += "/"
		}
		filename := path.Base(relPath)

		content, err := file.ReadContent()
		if err != nil {
			return err
		}

		isTestFile := strings.Contains(relPath, "__test__") || strings.Contains(relPath, "__tests__")
		isSQLFile := strings.HasSuffix(strings.ToLower(filename), ".sql")

		sources = append(sources, Source{
			Path:       "./" + relPath,
			Directory:  "./" + directory,
			Filename:   filename,
			Content:    string(content),
			IsSQLFile:  isSQLFile,
			IsTestFile: isTestFile,
		})
		return nil
	})

	return sources, err
}

func TestDiscovery_FullPipeline_FromFilesystem(t *testing.T) {
	fs := filesystem.NewMemoryFileSystem("/project")
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("__test__/_setup.sql", "-- fixture")
	fs.AddFile("__test__/test_one.sql", "SELECT 1;")
	fs.AddFile("__test__/nested/_setup.sql", "-- nested fixture")
	fs.AddFile("__test__/nested/test_two.sql", "SELECT 2;")

	sources, err := sourcesFromMemFS(fs, ".")
	if err != nil {
		t.Fatalf("Failed to scan: %v", err)
	}

	discoverer := NewDiscoverer(nil)
	tree, err := discoverer.Discover(sources)
	if err != nil {
		t.Fatalf("Failed to discover: %v", err)
	}

	resolver := func(p string) (string, error) {
		for _, src := range sources {
			if src.Path == p {
				return src.Content, nil
			}
		}
		return "", fmt.Errorf("not found: %s", p)
	}
	planBuilder := NewPlanBuilder(resolver)
	rows, err := planBuilder.Build(tree)
	if err != nil {
		t.Fatalf("Failed to build plan: %v", err)
	}

	validator := NewSavepointValidator()
	result := validator.Validate(rows)
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	fixtureCount := 0
	testCount := 0
	teardownCount := 0
	for _, row := range rows {
		switch row.StepType {
		case "fixture":
			fixtureCount++
		case "test":
			testCount++
		case "teardown":
			teardownCount++
		}
	}

	if fixtureCount != 2 {
		t.Errorf("Expected 2 fixtures, got %d", fixtureCount)
	}
	if testCount != 2 {
		t.Errorf("Expected 2 tests, got %d", testCount)
	}
	if teardownCount != 2 {
		t.Errorf("Expected 2 teardowns, got %d", teardownCount)
	}
}

func TestDiscovery_FullPipeline_DeepNesting(t *testing.T) {
	fs := filesystem.NewMemoryFileSystem("/project")
	fs.AddFile("deploy.sql", "SELECT 1;")

	depth := 5
	for d := 0; d < depth; d++ {
		var pathParts []string
		pathParts = append(pathParts, "__test__")
		for i := 1; i <= d; i++ {
			pathParts = append(pathParts, fmt.Sprintf("l%d", i))
		}
		basePath := strings.Join(pathParts, "/")

		fs.AddFile(basePath+"/_setup.sql", fmt.Sprintf("-- level %d fixture", d))
		fs.AddFile(basePath+"/test.sql", fmt.Sprintf("SELECT %d;", d))
	}

	sources, err := sourcesFromMemFS(fs, ".")
	if err != nil {
		t.Fatalf("Failed to scan: %v", err)
	}

	discoverer := NewDiscoverer(nil)
	tree, err := discoverer.Discover(sources)
	if err != nil {
		t.Fatalf("Failed to discover: %v", err)
	}

	resolver := func(p string) (string, error) {
		for _, src := range sources {
			if src.Path == p {
				return src.Content, nil
			}
		}
		return "", fmt.Errorf("not found: %s", p)
	}
	planBuilder := NewPlanBuilder(resolver)
	rows, err := planBuilder.Build(tree)
	if err != nil {
		t.Fatalf("Failed to build plan: %v", err)
	}

	validator := NewSavepointValidator()
	result := validator.Validate(rows)
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	fixtureCount := 0
	testCount := 0
	teardownCount := 0
	maxDepth := 0
	for _, row := range rows {
		switch row.StepType {
		case "fixture":
			fixtureCount++
		case "test":
			testCount++
		case "teardown":
			teardownCount++
		}
		if row.Depth > maxDepth {
			maxDepth = row.Depth
		}
	}

	if fixtureCount != depth {
		t.Errorf("Expected %d fixtures, got %d", depth, fixtureCount)
	}
	if testCount != depth {
		t.Errorf("Expected %d tests, got %d", depth, testCount)
	}
	if teardownCount != depth {
		t.Errorf("Expected %d teardowns, got %d", depth, teardownCount)
	}
	if maxDepth != depth-1 {
		t.Errorf("Expected max depth %d, got %d", depth-1, maxDepth)
	}
}

func TestDiscovery_FullPipeline_EmptyTestDirectory(t *testing.T) {
	fs := filesystem.NewMemoryFileSystem("/project")
	fs.AddFile("deploy.sql", "SELECT 1;")

	sources, err := sourcesFromMemFS(fs, ".")
	if err != nil {
		t.Fatalf("Failed to scan: %v", err)
	}

	discoverer := NewDiscoverer(nil)
	tree, err := discoverer.Discover(sources)
	if err != nil {
		t.Fatalf("Failed to discover: %v", err)
	}

	if !tree.IsEmpty() {
		t.Error("Expected empty tree when no __test__ directories")
	}

	resolver := func(p string) (string, error) {
		return "", fmt.Errorf("not found: %s", p)
	}
	planBuilder := NewPlanBuilder(resolver)
	rows, err := planBuilder.Build(tree)
	if err != nil {
		t.Fatalf("Failed to build plan: %v", err)
	}

	if len(rows) != 0 {
		t.Errorf("Expected 0 rows, got %d", len(rows))
	}

	validator := NewSavepointValidator()
	result := validator.Validate(rows)
	if !result.Valid {
		t.Fatalf("Validation should pass for empty plan: %v", result.Errors)
	}
}

func TestDiscovery_FullPipeline_FixtureOnlyDirectory(t *testing.T) {
	fs := filesystem.NewMemoryFileSystem("/project")
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("__test__/_setup.sql", "-- fixture only")

	sources, err := sourcesFromMemFS(fs, ".")
	if err != nil {
		t.Fatalf("Failed to scan: %v", err)
	}

	discoverer := NewDiscoverer(nil)
	tree, err := discoverer.Discover(sources)
	if err != nil {
		t.Fatalf("Failed to discover: %v", err)
	}

	resolver := func(p string) (string, error) {
		for _, src := range sources {
			if src.Path == p {
				return src.Content, nil
			}
		}
		return "", fmt.Errorf("not found: %s", p)
	}
	planBuilder := NewPlanBuilder(resolver)
	rows, err := planBuilder.Build(tree)
	if err != nil {
		t.Fatalf("Failed to build plan: %v", err)
	}

	validator := NewSavepointValidator()
	result := validator.Validate(rows)
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	fixtureCount := 0
	testCount := 0
	teardownCount := 0
	for _, row := range rows {
		switch row.StepType {
		case "fixture":
			fixtureCount++
		case "test":
			testCount++
		case "teardown":
			teardownCount++
		}
	}

	if fixtureCount != 1 {
		t.Errorf("Expected 1 fixture, got %d", fixtureCount)
	}
	if testCount != 0 {
		t.Errorf("Expected 0 tests, got %d", testCount)
	}
	if teardownCount != 1 {
		t.Errorf("Expected 1 teardown, got %d", teardownCount)
	}
}

func TestDiscovery_FullPipeline_TestsOnlyNoFixture(t *testing.T) {
	fs := filesystem.NewMemoryFileSystem("/project")
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("__test__/test_one.sql", "SELECT 1;")
	fs.AddFile("__test__/test_two.sql", "SELECT 2;")
	fs.AddFile("__test__/test_three.sql", "SELECT 3;")

	sources, err := sourcesFromMemFS(fs, ".")
	if err != nil {
		t.Fatalf("Failed to scan: %v", err)
	}

	discoverer := NewDiscoverer(nil)
	tree, err := discoverer.Discover(sources)
	if err != nil {
		t.Fatalf("Failed to discover: %v", err)
	}

	resolver := func(p string) (string, error) {
		for _, src := range sources {
			if src.Path == p {
				return src.Content, nil
			}
		}
		return "", fmt.Errorf("not found: %s", p)
	}
	planBuilder := NewPlanBuilder(resolver)
	rows, err := planBuilder.Build(tree)
	if err != nil {
		t.Fatalf("Failed to build plan: %v", err)
	}

	validator := NewSavepointValidator()
	result := validator.Validate(rows)
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	fixtureCount := 0
	testCount := 0
	teardownCount := 0
	for _, row := range rows {
		switch row.StepType {
		case "fixture":
			fixtureCount++
		case "test":
			testCount++
		case "teardown":
			teardownCount++
		}
	}

	if fixtureCount != 0 {
		t.Errorf("Expected 0 fixtures, got %d", fixtureCount)
	}
	if testCount != 3 {
		t.Errorf("Expected 3 tests, got %d", testCount)
	}
	if teardownCount != 1 {
		t.Errorf("Expected 1 teardown, got %d", teardownCount)
	}
}

func TestDiscovery_FullPipeline_SpecialCharactersInPaths(t *testing.T) {
	fs := filesystem.NewMemoryFileSystem("/project")
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("__test__/test_with_numbers_123.sql", "SELECT 1;")
	fs.AddFile("__test__/test_with_underscores_a_b_c.sql", "SELECT 2;")

	sources, err := sourcesFromMemFS(fs, ".")
	if err != nil {
		t.Fatalf("Failed to scan: %v", err)
	}

	discoverer := NewDiscoverer(nil)
	tree, err := discoverer.Discover(sources)
	if err != nil {
		t.Fatalf("Failed to discover: %v", err)
	}

	resolver := func(p string) (string, error) {
		for _, src := range sources {
			if src.Path == p {
				return src.Content, nil
			}
		}
		return "", fmt.Errorf("not found: %s", p)
	}
	planBuilder := NewPlanBuilder(resolver)
	rows, err := planBuilder.Build(tree)
	if err != nil {
		t.Fatalf("Failed to build plan: %v", err)
	}

	validator := NewSavepointValidator()
	result := validator.Validate(rows)
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	testCount := 0
	for _, row := range rows {
		if row.StepType == "test" {
			testCount++
		}
	}

	if testCount != 2 {
		t.Errorf("Expected 2 tests, got %d", testCount)
	}
}

func TestDiscovery_FullPipeline_MultipleBranches(t *testing.T) {
	fs := filesystem.NewMemoryFileSystem("/project")
	fs.AddFile("deploy.sql", "SELECT 1;")

	fs.AddFile("users/__test__/_setup.sql", "-- users fixture")
	fs.AddFile("users/__test__/test_create.sql", "SELECT 1;")
	fs.AddFile("users/__test__/test_update.sql", "SELECT 2;")

	fs.AddFile("orders/__test__/_setup.sql", "-- orders fixture")
	fs.AddFile("orders/__test__/test_create.sql", "SELECT 1;")

	fs.AddFile("products/__test__/test_list.sql", "SELECT 1;")

	sources, err := sourcesFromMemFS(fs, ".")
	if err != nil {
		t.Fatalf("Failed to scan: %v", err)
	}

	discoverer := NewDiscoverer(nil)
	tree, err := discoverer.Discover(sources)
	if err != nil {
		t.Fatalf("Failed to discover: %v", err)
	}

	if len(tree.Directories) != 3 {
		t.Errorf("Expected 3 top-level directories, got %d", len(tree.Directories))
	}

	resolver := func(p string) (string, error) {
		for _, src := range sources {
			if src.Path == p {
				return src.Content, nil
			}
		}
		return "", fmt.Errorf("not found: %s", p)
	}
	planBuilder := NewPlanBuilder(resolver)
	rows, err := planBuilder.Build(tree)
	if err != nil {
		t.Fatalf("Failed to build plan: %v", err)
	}

	validator := NewSavepointValidator()
	result := validator.Validate(rows)
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	fixtureCount := 0
	testCount := 0
	teardownCount := 0
	for _, row := range rows {
		switch row.StepType {
		case "fixture":
			fixtureCount++
		case "test":
			testCount++
		case "teardown":
			teardownCount++
		}
	}

	if fixtureCount != 2 {
		t.Errorf("Expected 2 fixtures (users + orders), got %d", fixtureCount)
	}
	if testCount != 4 {
		t.Errorf("Expected 4 tests, got %d", testCount)
	}
	if teardownCount != 3 {
		t.Errorf("Expected 3 teardowns, got %d", teardownCount)
	}
}

func TestDiscovery_FullPipeline_NonSQLFilesIgnored(t *testing.T) {
	fs := filesystem.NewMemoryFileSystem("/project")
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("__test__/_setup.sql", "-- fixture")
	fs.AddFile("__test__/test_one.sql", "SELECT 1;")
	fs.AddFile("__test__/README.md", "# Test docs")
	fs.AddFile("__test__/config.json", `{"key": "value"}`)
	fs.AddFile("__test__/.gitkeep", "")

	sources, err := sourcesFromMemFS(fs, ".")
	if err != nil {
		t.Fatalf("Failed to scan: %v", err)
	}

	discoverer := NewDiscoverer(nil)
	tree, err := discoverer.Discover(sources)
	if err != nil {
		t.Fatalf("Failed to discover: %v", err)
	}

	resolver := func(p string) (string, error) {
		for _, src := range sources {
			if src.Path == p {
				return src.Content, nil
			}
		}
		return "", fmt.Errorf("not found: %s", p)
	}
	planBuilder := NewPlanBuilder(resolver)
	rows, err := planBuilder.Build(tree)
	if err != nil {
		t.Fatalf("Failed to build plan: %v", err)
	}

	validator := NewSavepointValidator()
	result := validator.Validate(rows)
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	for _, row := range rows {
		if row.ScriptPath != nil && !strings.HasSuffix(*row.ScriptPath, ".sql") {
			t.Errorf("Non-SQL file included in plan: %s", *row.ScriptPath)
		}
	}
}
