package testdiscovery

import (
	"testing"
)

func TestNewPatternMatcher(t *testing.T) {
	m := NewPatternMatcher()
	if m == nil {
		t.Fatal("NewPatternMatcher() returned nil")
	}
}

func TestPatternMatcher_Matches_EmptyPattern(t *testing.T) {
	m := NewPatternMatcher()

	tests := []string{
		"./users/__test__/01_test.sql",
		"./api/__test__/nested/test.sql",
		"anything",
	}

	for _, path := range tests {
		if !m.Matches("", path) {
			t.Errorf("Empty pattern should match %q", path)
		}
	}
}

func TestPatternMatcher_Matches_ExactPath(t *testing.T) {
	m := NewPatternMatcher()

	if !m.Matches("./users/__test__/01_test.sql", "./users/__test__/01_test.sql") {
		t.Error("Exact path should match")
	}
	if m.Matches("./users/__test__/01_test.sql", "./users/__test__/02_test.sql") {
		t.Error("Different path should not match")
	}
}

func TestPatternMatcher_Matches_SingleStar(t *testing.T) {
	m := NewPatternMatcher()

	// Single * matches one path segment
	pattern := "./users/*"

	if !m.Matches(pattern, "./users/__test__") {
		t.Error("* should match single segment")
	}
	if !m.Matches(pattern, "./users/foo") {
		t.Error("* should match any segment")
	}
	if m.Matches(pattern, "./users/__test__/nested") {
		t.Error("* should not match multiple segments")
	}
	if m.Matches(pattern, "./api/__test__") {
		t.Error("* should not match wrong prefix")
	}
}

func TestPatternMatcher_Matches_SingleStarInMiddle(t *testing.T) {
	m := NewPatternMatcher()

	pattern := "./users/*/__test__"

	if !m.Matches(pattern, "./users/admin/__test__") {
		t.Error("* in middle should match segment")
	}
	if !m.Matches(pattern, "./users/guest/__test__") {
		t.Error("* in middle should match any segment")
	}
	if m.Matches(pattern, "./users/admin/super/__test__") {
		t.Error("* should not match multiple segments")
	}
}

func TestPatternMatcher_Matches_DoubleStar(t *testing.T) {
	m := NewPatternMatcher()

	// ** matches zero or more path segments
	pattern := "./users/**"

	if !m.Matches(pattern, "./users/__test__") {
		t.Error("** should match single level")
	}
	if !m.Matches(pattern, "./users/__test__/nested") {
		t.Error("** should match nested levels")
	}
	if !m.Matches(pattern, "./users/__test__/deep/nested/path") {
		t.Error("** should match deep nesting")
	}
	if !m.Matches(pattern, "./users") {
		t.Error("** should match zero segments")
	}
	if m.Matches(pattern, "./api/__test__") {
		t.Error("** should not match wrong prefix")
	}
}

func TestPatternMatcher_Matches_DoubleStarInMiddle(t *testing.T) {
	m := NewPatternMatcher()

	pattern := "./users/**/__test__"

	if !m.Matches(pattern, "./users/__test__") {
		t.Error("** in middle should match zero segments")
	}
	if !m.Matches(pattern, "./users/admin/__test__") {
		t.Error("** in middle should match one segment")
	}
	if !m.Matches(pattern, "./users/admin/super/__test__") {
		t.Error("** in middle should match multiple segments")
	}
}

func TestPatternMatcher_Matches_FileExtension(t *testing.T) {
	m := NewPatternMatcher()

	pattern := "./**/*.sql"

	if !m.Matches(pattern, "./users/__test__/01_test.sql") {
		t.Error("Should match .sql file")
	}
	if m.Matches(pattern, "./users/__test__/readme.md") {
		t.Error("Should not match .md file")
	}
}

func TestPatternMatcher_Matches_QuestionMark(t *testing.T) {
	m := NewPatternMatcher()

	pattern := "./test/0?_test.sql"

	if !m.Matches(pattern, "./test/01_test.sql") {
		t.Error("? should match single char")
	}
	if !m.Matches(pattern, "./test/02_test.sql") {
		t.Error("? should match any single char")
	}
	if m.Matches(pattern, "./test/001_test.sql") {
		t.Error("? should not match multiple chars")
	}
}

func TestFilterByPattern_EmptyPattern(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: Ptr("./a/__test__/_setup.sql"), Directory: "./a/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: Ptr("./a/__test__/01.sql"), Directory: "./a/__test__"},
		{Ordinal: 3, StepType: "teardown", Directory: "./a/__test__"},
	}

	result := FilterByPattern(rows, "")
	if len(result) != 3 {
		t.Errorf("Empty pattern should return all rows, got %d", len(result))
	}
}

func TestFilterByPattern_MatchesDirectory(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: Ptr("./a/__test__/_setup.sql"), Directory: "./a/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: Ptr("./a/__test__/01.sql"), Directory: "./a/__test__"},
		{Ordinal: 3, StepType: "teardown", Directory: "./a/__test__"},
		{Ordinal: 4, StepType: "fixture", ScriptPath: Ptr("./b/__test__/_setup.sql"), Directory: "./b/__test__"},
		{Ordinal: 5, StepType: "test", ScriptPath: Ptr("./b/__test__/01.sql"), Directory: "./b/__test__"},
		{Ordinal: 6, StepType: "teardown", Directory: "./b/__test__"},
	}

	result := FilterByPattern(rows, "./a/**")

	// Should only include ./a/__test__ directory
	for _, row := range result {
		if row.Directory != "./a/__test__" {
			t.Errorf("Should only include ./a/__test__, got directory %q", row.Directory)
		}
	}
}

func TestFilterByPattern_PreservesStructure(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: Ptr("./a/__test__/_setup.sql"), Directory: "./a/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: Ptr("./a/__test__/01.sql"), Directory: "./a/__test__"},
		{Ordinal: 3, StepType: "teardown", Directory: "./a/__test__"},
	}

	result := FilterByPattern(rows, "./a/**")

	// Verify structure: fixture, test, teardown
	if len(result) < 3 {
		t.Fatalf("Expected at least 3 rows, got %d", len(result))
	}

	hasFixture := false
	hasTest := false
	hasTeardown := false
	for _, row := range result {
		switch row.StepType {
		case "fixture":
			hasFixture = true
		case "test":
			hasTest = true
		case "teardown":
			hasTeardown = true
		}
	}

	if !hasFixture || !hasTest || !hasTeardown {
		t.Error("Filtered result should preserve structure")
	}
}

func TestFilterByPattern_NestedDirectories(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: Ptr("./users/__test__/_setup.sql"), Directory: "./users/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: Ptr("./users/__test__/01.sql"), Directory: "./users/__test__"},
		{Ordinal: 3, StepType: "fixture", ScriptPath: Ptr("./users/__test__/admin/_setup.sql"), Directory: "./users/__test__/admin"},
		{Ordinal: 4, StepType: "test", ScriptPath: Ptr("./users/__test__/admin/01.sql"), Directory: "./users/__test__/admin"},
		{Ordinal: 5, StepType: "teardown", Directory: "./users/__test__/admin"},
		{Ordinal: 6, StepType: "teardown", Directory: "./users/__test__"},
	}

	result := FilterByPattern(rows, "./users/__test__/admin/**")

	// Should include only admin directory
	for _, row := range result {
		if row.Directory != "./users/__test__/admin" {
			t.Errorf("Should only include admin dir, got %q", row.Directory)
		}
	}
}

func TestFilterByPattern_NoMatches(t *testing.T) {
	rows := []TestScriptRow{
		{Ordinal: 1, StepType: "fixture", ScriptPath: Ptr("./a/__test__/_setup.sql"), Directory: "./a/__test__"},
		{Ordinal: 2, StepType: "test", ScriptPath: Ptr("./a/__test__/01.sql"), Directory: "./a/__test__"},
		{Ordinal: 3, StepType: "teardown", Directory: "./a/__test__"},
	}

	result := FilterByPattern(rows, "./nonexistent/**")

	if len(result) != 0 {
		t.Errorf("No matches should return empty, got %d rows", len(result))
	}
}
