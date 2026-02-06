package testdiscovery

import (
	"fmt"
	"sort"
	"strings"
)

// DiscoveryConfig configures how fixtures are detected.
type DiscoveryConfig struct {
	FixtureNames     []string // Exact filenames that indicate a fixture (default: ["_setup.sql"])
	FixturePrefixes  []string // Filename prefixes that indicate a fixture (default: ["00_"])
	FixtureSubstring string   // Substring in filename that indicates a fixture (default: "fixture")
}

// DefaultConfig returns the default discovery configuration.
func DefaultConfig() *DiscoveryConfig {
	return &DiscoveryConfig{
		FixtureNames:     []string{"_setup.sql", "_setup.psql"},
		FixturePrefixes:  []string{"00_"},
		FixtureSubstring: "fixture",
	}
}

// Discoverer traverses source files and builds a TestTree.
type Discoverer struct {
	config *DiscoveryConfig
}

// NewDiscoverer creates a new Discoverer with the given configuration.
// If config is nil, defaults are used.
func NewDiscoverer(config *DiscoveryConfig) *Discoverer {
	if config == nil {
		config = DefaultConfig()
	}
	return &Discoverer{config: config}
}

// Discover processes source files and returns a TestTree.
// Only files with IsTestFile=true and IsSQLFile=true are considered.
func (d *Discoverer) Discover(sources []Source) (*TestTree, error) {
	tree := NewTestTree()

	if len(sources) == 0 {
		return tree, nil
	}

	// Group files by directory
	dirFiles := make(map[string][]Source)
	for _, src := range sources {
		if !src.IsTestFile || !src.IsSQLFile {
			continue
		}
		dirFiles[src.Directory] = append(dirFiles[src.Directory], src)
	}

	if len(dirFiles) == 0 {
		return tree, nil
	}

	// Build TestDirectory for each directory
	allDirs := make(map[string]*TestDirectory)
	for dirPath, files := range dirFiles {
		dir, err := d.buildDirectory(dirPath, files)
		if err != nil {
			return nil, err
		}
		allDirs[dirPath] = dir
	}

	// Establish parent-child relationships and identify top-level directories
	topLevel := d.buildHierarchy(allDirs)

	// Sort directories and add to tree
	sort.Slice(topLevel, func(i, j int) bool {
		return topLevel[i].Path < topLevel[j].Path
	})
	for _, dir := range topLevel {
		tree.AddDirectory(dir)
	}

	return tree, nil
}

// buildDirectory creates a TestDirectory from source files.
func (d *Discoverer) buildDirectory(dirPath string, files []Source) (*TestDirectory, error) {
	// Keep directory path as-is (with trailing /)
	dir := NewTestDirectory(dirPath, 0)

	var fixtures []*TestFile
	var tests []*TestFile

	for _, src := range files {
		file := &TestFile{
			Path:      src.Path,
			Filename:  src.Filename,
			Directory: src.Directory,
			IsFixture: d.isFixture(src.Filename),
		}

		if file.IsFixture {
			fixtures = append(fixtures, file)
		} else {
			tests = append(tests, file)
		}
	}

	// Check for ambiguous fixtures
	if len(fixtures) > 1 {
		return nil, fmt.Errorf("ambiguous fixtures in %s: found %d fixture files", dirPath, len(fixtures))
	}

	if len(fixtures) == 1 {
		dir.SetFixture(fixtures[0])
	}

	// Sort tests by filename
	sort.Slice(tests, func(i, j int) bool {
		return tests[i].Filename < tests[j].Filename
	})

	for _, t := range tests {
		dir.AddTest(t)
	}

	return dir, nil
}

// isFixture determines if a filename indicates a fixture file.
func (d *Discoverer) isFixture(filename string) bool {
	lower := strings.ToLower(filename)

	// Check exact names first (e.g., _setup.sql)
	for _, name := range d.config.FixtureNames {
		if lower == strings.ToLower(name) {
			return true
		}
	}

	// Check prefixes (e.g., 00_)
	for _, prefix := range d.config.FixturePrefixes {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return true
		}
	}

	// Check substring (e.g., "fixture" in filename)
	if d.config.FixtureSubstring != "" {
		if strings.Contains(lower, strings.ToLower(d.config.FixtureSubstring)) {
			return true
		}
	}

	return false
}

// buildHierarchy establishes parent-child relationships and returns top-level directories.
func (d *Discoverer) buildHierarchy(allDirs map[string]*TestDirectory) []*TestDirectory {
	// Sort paths to process parents before children
	paths := make([]string, 0, len(allDirs))
	for p := range allDirs {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var topLevel []*TestDirectory
	pathToDir := make(map[string]*TestDirectory)

	for _, dirPath := range paths {
		dir := allDirs[dirPath]
		path := dir.Path

		// Find parent by checking if any existing directory is a prefix
		var parent *TestDirectory
		for existingPath, existingDir := range pathToDir {
			if isParentPath(existingPath, path) {
				if parent == nil || len(existingPath) > len(parent.Path) {
					parent = existingDir
				}
			}
		}

		if parent != nil {
			dir.Depth = parent.Depth + 1
			parent.AddChild(dir)
		} else {
			topLevel = append(topLevel, dir)
		}

		pathToDir[path] = dir
	}

	// Sort children within each directory
	for _, dir := range pathToDir {
		d.sortChildren(dir)
	}

	return topLevel
}

// isParentPath checks if parentPath is a parent directory of childPath.
// Both paths are expected to have trailing slashes (e.g., "./__test__/", "./__test__/auth/").
func isParentPath(parentPath, childPath string) bool {
	if !strings.HasPrefix(childPath, parentPath) {
		return false
	}
	// With trailing slashes, parent is a prefix and child is longer
	return len(childPath) > len(parentPath)
}

// sortChildren recursively sorts child directories by path.
func (d *Discoverer) sortChildren(dir *TestDirectory) {
	if len(dir.Children) == 0 {
		return
	}
	sort.Slice(dir.Children, func(i, j int) bool {
		return dir.Children[i].Path < dir.Children[j].Path
	})
	for _, child := range dir.Children {
		d.sortChildren(child)
	}
}
