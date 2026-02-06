package testdiscovery

import (
	"fmt"
	"testing"
)

func TestNewDiscoverer(t *testing.T) {
	d := NewDiscoverer(nil)
	if d == nil {
		t.Fatal("NewDiscoverer() returned nil")
	}
}

func TestDiscoverer_Discover_EmptyInput(t *testing.T) {
	d := NewDiscoverer(nil)
	tree, err := d.Discover(nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if tree == nil {
		t.Fatal("Discover() returned nil tree")
	}
	if !tree.IsEmpty() {
		t.Error("Tree should be empty for nil input")
	}
}

func TestDiscoverer_Discover_NoTestFiles(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{Path: "./users/schema.sql", IsSQLFile: true, IsTestFile: false},
		{Path: "./api/routes.sql", IsSQLFile: true, IsTestFile: false},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !tree.IsEmpty() {
		t.Error("Tree should be empty when no test files")
	}
}

func TestDiscoverer_Discover_SingleTest(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{
			Path:       "./users/__test__/01_test_create.sql",
			Directory:  "./users/__test__/",
			Filename:   "01_test_create.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if tree.IsEmpty() {
		t.Fatal("Tree should not be empty")
	}
	if len(tree.Directories) != 1 {
		t.Fatalf("Expected 1 directory, got %d", len(tree.Directories))
	}

	dir := tree.Directories[0]
	if dir.Path != "./users/__test__" {
		t.Errorf("Directory path = %q, expected %q", dir.Path, "./users/__test__")
	}
	if dir.HasFixture() {
		t.Error("Should not have fixture")
	}
	if len(dir.Tests) != 1 {
		t.Errorf("Expected 1 test, got %d", len(dir.Tests))
	}
}

func TestDiscoverer_Discover_FixtureOnly(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{
			Path:       "./users/__test__/00_fixture.sql",
			Directory:  "./users/__test__/",
			Filename:   "00_fixture.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	dir := tree.Directories[0]
	if !dir.HasFixture() {
		t.Error("Should have fixture")
	}
	if dir.Fixture.Filename != "00_fixture.sql" {
		t.Errorf("Fixture filename = %q, expected %q", dir.Fixture.Filename, "00_fixture.sql")
	}
	if len(dir.Tests) != 0 {
		t.Errorf("Expected 0 tests, got %d", len(dir.Tests))
	}
}

func TestDiscoverer_Discover_FixtureAndTests(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{
			Path:       "./users/__test__/00_fixture.sql",
			Directory:  "./users/__test__/",
			Filename:   "00_fixture.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./users/__test__/01_test_create.sql",
			Directory:  "./users/__test__/",
			Filename:   "01_test_create.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./users/__test__/02_test_delete.sql",
			Directory:  "./users/__test__/",
			Filename:   "02_test_delete.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	dir := tree.Directories[0]
	if !dir.HasFixture() {
		t.Error("Should have fixture")
	}
	if len(dir.Tests) != 2 {
		t.Fatalf("Expected 2 tests, got %d", len(dir.Tests))
	}

	// Verify test order
	if dir.Tests[0].Filename != "01_test_create.sql" {
		t.Errorf("First test = %q, expected %q", dir.Tests[0].Filename, "01_test_create.sql")
	}
	if dir.Tests[1].Filename != "02_test_delete.sql" {
		t.Errorf("Second test = %q, expected %q", dir.Tests[1].Filename, "02_test_delete.sql")
	}
}

func TestDiscoverer_Discover_TestsOrdered(t *testing.T) {
	d := NewDiscoverer(nil)
	// Provide in non-alphabetical order
	sources := []Source{
		{
			Path:       "./test/__test__/03_z_test.sql",
			Directory:  "./test/__test__/",
			Filename:   "03_z_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./test/__test__/01_a_test.sql",
			Directory:  "./test/__test__/",
			Filename:   "01_a_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./test/__test__/02_m_test.sql",
			Directory:  "./test/__test__/",
			Filename:   "02_m_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	dir := tree.Directories[0]
	if len(dir.Tests) != 3 {
		t.Fatalf("Expected 3 tests, got %d", len(dir.Tests))
	}

	// Should be sorted alphabetically
	expected := []string{"01_a_test.sql", "02_m_test.sql", "03_z_test.sql"}
	for i, exp := range expected {
		if dir.Tests[i].Filename != exp {
			t.Errorf("Tests[%d].Filename = %q, expected %q", i, dir.Tests[i].Filename, exp)
		}
	}
}

func TestDiscoverer_Discover_SetupFile(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{
			Path:       "./test/__test__/_setup.sql",
			Directory:  "./test/__test__/",
			Filename:   "_setup.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./test/__test__/test_something.sql",
			Directory:  "./test/__test__/",
			Filename:   "test_something.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	dir := tree.Directories[0]
	if !dir.HasFixture() {
		t.Error("Should detect _setup.sql as fixture")
	}
	if dir.Fixture.Filename != "_setup.sql" {
		t.Errorf("Fixture = %q, expected %q", dir.Fixture.Filename, "_setup.sql")
	}
	if len(dir.Tests) != 1 {
		t.Errorf("Tests count = %d, expected 1", len(dir.Tests))
	}
}

func TestDiscoverer_Discover_FixtureBySubstring(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{
			Path:       "./test/__test__/my_fixture_setup.sql",
			Directory:  "./test/__test__/",
			Filename:   "my_fixture_setup.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./test/__test__/test_something.sql",
			Directory:  "./test/__test__/",
			Filename:   "test_something.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	dir := tree.Directories[0]
	if !dir.HasFixture() {
		t.Error("Should detect fixture by substring")
	}
	if dir.Fixture.Filename != "my_fixture_setup.sql" {
		t.Errorf("Fixture = %q, expected %q", dir.Fixture.Filename, "my_fixture_setup.sql")
	}
}

func TestDiscoverer_Discover_FixtureCaseInsensitive(t *testing.T) {
	d := NewDiscoverer(nil)

	testCases := []string{"FIXTURE.sql", "Fixture.sql", "FiXtUrE.sql", "fixture.sql"}

	for _, filename := range testCases {
		sources := []Source{
			{
				Path:       "./test/__test__/" + filename,
				Directory:  "./test/__test__/",
				Filename:   filename,
				IsSQLFile:  true,
				IsTestFile: true,
			},
		}

		tree, err := d.Discover(sources)
		if err != nil {
			t.Fatalf("Discover() error for %q: %v", filename, err)
		}

		dir := tree.Directories[0]
		if !dir.HasFixture() {
			t.Errorf("Should detect %q as fixture (case insensitive)", filename)
		}
	}
}

func TestDiscoverer_Discover_AmbiguousFixture(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{
			Path:       "./test/__test__/00_setup.sql",
			Directory:  "./test/__test__/",
			Filename:   "00_setup.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./test/__test__/00_fixture.sql",
			Directory:  "./test/__test__/",
			Filename:   "00_fixture.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	_, err := d.Discover(sources)
	if err == nil {
		t.Error("Expected error for ambiguous fixtures")
	}
}

func TestDiscoverer_Discover_MultipleDirectories(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{
			Path:       "./users/__test__/01_test.sql",
			Directory:  "./users/__test__/",
			Filename:   "01_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./api/__test__/01_test.sql",
			Directory:  "./api/__test__/",
			Filename:   "01_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./db/__test__/01_test.sql",
			Directory:  "./db/__test__/",
			Filename:   "01_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	if len(tree.Directories) != 3 {
		t.Errorf("Expected 3 directories, got %d", len(tree.Directories))
	}
}

func TestDiscoverer_Discover_NestedDirectories(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		// Parent directory
		{
			Path:       "./users/__test__/00_fixture.sql",
			Directory:  "./users/__test__/",
			Filename:   "00_fixture.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./users/__test__/01_test.sql",
			Directory:  "./users/__test__/",
			Filename:   "01_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		// Nested directory
		{
			Path:       "./users/__test__/admin/00_fixture.sql",
			Directory:  "./users/__test__/admin/",
			Filename:   "00_fixture.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./users/__test__/admin/01_test.sql",
			Directory:  "./users/__test__/admin/",
			Filename:   "01_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	// Should have 1 top-level directory with 1 child
	if len(tree.Directories) != 1 {
		t.Fatalf("Expected 1 top-level directory, got %d", len(tree.Directories))
	}

	parent := tree.Directories[0]
	if parent.Path != "./users/__test__" {
		t.Errorf("Parent path = %q, expected %q", parent.Path, "./users/__test__")
	}
	if !parent.HasFixture() {
		t.Error("Parent should have fixture")
	}
	if len(parent.Tests) != 1 {
		t.Errorf("Parent should have 1 test, got %d", len(parent.Tests))
	}

	if len(parent.Children) != 1 {
		t.Fatalf("Expected 1 child, got %d", len(parent.Children))
	}

	child := parent.Children[0]
	if child.Path != "./users/__test__/admin" {
		t.Errorf("Child path = %q, expected %q", child.Path, "./users/__test__/admin")
	}
	if !child.HasFixture() {
		t.Error("Child should have fixture")
	}
	if child.Depth != 1 {
		t.Errorf("Child depth = %d, expected 1", child.Depth)
	}
}

func TestDiscoverer_Discover_SkipsNonSQLFiles(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{
			Path:       "./test/__test__/readme.md",
			Directory:  "./test/__test__/",
			Filename:   "readme.md",
			IsSQLFile:  false, // Not SQL
			IsTestFile: true,
		},
		{
			Path:       "./test/__test__/01_test.sql",
			Directory:  "./test/__test__/",
			Filename:   "01_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	dir := tree.Directories[0]
	if len(dir.Tests) != 1 {
		t.Errorf("Expected 1 test (skipping .md), got %d", len(dir.Tests))
	}
}

func TestDiscoverer_Discover_DirectoriesOrdered(t *testing.T) {
	d := NewDiscoverer(nil)
	// Provide in non-alphabetical order
	sources := []Source{
		{
			Path:       "./z/__test__/test.sql",
			Directory:  "./z/__test__/",
			Filename:   "test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./a/__test__/test.sql",
			Directory:  "./a/__test__/",
			Filename:   "test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./m/__test__/test.sql",
			Directory:  "./m/__test__/",
			Filename:   "test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	// Should be sorted
	expected := []string{"./a/__test__", "./m/__test__", "./z/__test__"}
	for i, exp := range expected {
		if tree.Directories[i].Path != exp {
			t.Errorf("Directories[%d].Path = %q, expected %q", i, tree.Directories[i].Path, exp)
		}
	}
}

func TestDiscoverer_Discover_CustomConfig(t *testing.T) {
	config := &DiscoveryConfig{
		FixturePrefixes:  []string{"setup_", "init_"},
		FixtureSubstring: "bootstrap",
	}
	d := NewDiscoverer(config)

	sources := []Source{
		{
			Path:       "./test/__test__/setup_data.sql",
			Directory:  "./test/__test__/",
			Filename:   "setup_data.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
		{
			Path:       "./test/__test__/01_test.sql",
			Directory:  "./test/__test__/",
			Filename:   "01_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	dir := tree.Directories[0]
	if !dir.HasFixture() {
		t.Error("Should detect 'setup_' prefix as fixture with custom config")
	}
}

func TestDiscoverer_Discover_TestsVariant(t *testing.T) {
	d := NewDiscoverer(nil)
	sources := []Source{
		{
			Path:       "./users/__tests__/01_test.sql", // __tests__ variant
			Directory:  "./users/__tests__/",
			Filename:   "01_test.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		},
	}

	tree, err := d.Discover(sources)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	if tree.IsEmpty() {
		t.Error("Should recognize __tests__ directory")
	}
}

func BenchmarkDiscoverer_Discover(b *testing.B) {
	d := NewDiscoverer(nil)

	// Generate 100 test directories with 10 files each
	var sources []Source
	for i := 0; i < 100; i++ {
		dir := fmt.Sprintf("./module%d/__test__/", i)
		sources = append(sources, Source{
			Path:       dir + "00_fixture.sql",
			Directory:  dir,
			Filename:   "00_fixture.sql",
			IsSQLFile:  true,
			IsTestFile: true,
		})
		for j := 1; j <= 9; j++ {
			sources = append(sources, Source{
				Path:       fmt.Sprintf("%s%02d_test.sql", dir, j),
				Directory:  dir,
				Filename:   fmt.Sprintf("%02d_test.sql", j),
				IsSQLFile:  true,
				IsTestFile: true,
			})
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Discover(sources)
	}
}
