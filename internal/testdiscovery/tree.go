package testdiscovery

// Source represents a file from pgmi_source table.
// This is the input to the test discovery process.
type Source struct {
	Path       string // Full relative path, e.g., "./users/__test__/01_test.sql"
	Directory  string // Parent directory with trailing slash, e.g., "./users/__test__/"
	Filename   string // Just the filename, e.g., "01_test.sql"
	Content    string // File contents
	IsSQLFile  bool   // True if recognized SQL extension
	IsTestFile bool   // True if path contains /__test__/ or /__tests__/
}

// TestTree represents the discovered test hierarchy.
// It contains all top-level __test__/ directories found in the project.
type TestTree struct {
	Directories []*TestDirectory // Top-level test directories
}

// TestDirectory represents a single __test__/ directory and its contents.
type TestDirectory struct {
	Path     string           // Directory path, e.g., "./users/__test__"
	Fixture  *TestFile        // Fixture file, nil if none
	Tests    []*TestFile      // Test files, ordered by filename
	Children []*TestDirectory // Nested __test__/ directories
	Depth    int              // Nesting level (0 = top-level)
}

// TestFile represents a single test or fixture SQL file.
type TestFile struct {
	Path      string // Full path, e.g., "./users/__test__/01_test_create.sql"
	Filename  string // Just filename, e.g., "01_test_create.sql"
	Directory string // Parent directory, e.g., "./users/__test__/"
	IsFixture bool   // True if this is a fixture file
}

// TestScriptRow represents a row in the pgmi_test_script table.
// This is the output of the plan builder - an ordered execution plan.
type TestScriptRow struct {
	SortKey    int     // Execution order (1-based)
	Path       *string // File path, nil for control-only rows
	ScriptType string  // "fixture", "test", or "rollback"
	BeforeExec *string // SQL to execute before file (e.g., SAVEPOINT)
	AfterExec  *string // SQL to execute after file (e.g., ROLLBACK TO)
	Directory  string  // Parent __test__/ directory
	Depth      int     // Nesting level
}

// NewTestTree creates an empty TestTree.
func NewTestTree() *TestTree {
	return &TestTree{
		Directories: make([]*TestDirectory, 0),
	}
}

// NewTestDirectory creates a new TestDirectory with the given path and depth.
func NewTestDirectory(path string, depth int) *TestDirectory {
	return &TestDirectory{
		Path:     path,
		Tests:    make([]*TestFile, 0),
		Children: make([]*TestDirectory, 0),
		Depth:    depth,
	}
}

// AddDirectory adds a test directory to the tree.
func (t *TestTree) AddDirectory(dir *TestDirectory) {
	t.Directories = append(t.Directories, dir)
}

// IsEmpty returns true if the tree has no directories.
func (t *TestTree) IsEmpty() bool {
	return len(t.Directories) == 0
}

// TotalTests returns the total number of test files across all directories.
func (t *TestTree) TotalTests() int {
	count := 0
	for _, dir := range t.Directories {
		count += dir.TotalTests()
	}
	return count
}

// TotalFixtures returns the total number of fixture files across all directories.
func (t *TestTree) TotalFixtures() int {
	count := 0
	for _, dir := range t.Directories {
		count += dir.TotalFixtures()
	}
	return count
}

// AddTest adds a test file to the directory.
func (d *TestDirectory) AddTest(file *TestFile) {
	d.Tests = append(d.Tests, file)
}

// AddChild adds a nested test directory.
func (d *TestDirectory) AddChild(child *TestDirectory) {
	d.Children = append(d.Children, child)
}

// SetFixture sets the fixture file for this directory.
func (d *TestDirectory) SetFixture(file *TestFile) {
	d.Fixture = file
}

// HasFixture returns true if this directory has a fixture.
func (d *TestDirectory) HasFixture() bool {
	return d.Fixture != nil
}

// TotalTests returns the count of test files in this directory and all children.
func (d *TestDirectory) TotalTests() int {
	count := len(d.Tests)
	for _, child := range d.Children {
		count += child.TotalTests()
	}
	return count
}

// TotalFixtures returns the count of fixtures in this directory and all children.
func (d *TestDirectory) TotalFixtures() int {
	count := 0
	if d.Fixture != nil {
		count = 1
	}
	for _, child := range d.Children {
		count += child.TotalFixtures()
	}
	return count
}

// AllFiles returns all files (fixture + tests) in order.
func (d *TestDirectory) AllFiles() []*TestFile {
	var files []*TestFile
	if d.Fixture != nil {
		files = append(files, d.Fixture)
	}
	files = append(files, d.Tests...)
	return files
}

// Ptr is a helper to create a pointer to a string.
func Ptr(s string) *string {
	return &s
}
