package loader

import (
	"testing"
)

func TestExtractTestDirectory_NestedPaths(t *testing.T) {
	testCases := []struct {
		path     string
		expected string
	}{
		{"./__test__/_setup.sql", "./__test__/"},
		{"./__test__/test_foo.sql", "./__test__/"},
		{"./__test__/child/_setup.sql", "./__test__/child/"},
		{"./__test__/child/test_child.sql", "./__test__/child/"},
		{"./__test__/child/grandchild/_setup.sql", "./__test__/child/grandchild/"},
		{"./__test__/child/grandchild/test_deep.sql", "./__test__/child/grandchild/"},
		// Non-test paths should return empty
		{"./migrations/001.sql", ""},
	}

	for _, tc := range testCases {
		result := extractTestDirectory(tc.path)
		if result != tc.expected {
			t.Errorf("extractTestDirectory(%q) = %q, want %q", tc.path, result, tc.expected)
		}
	}
}

func TestCountTestDirectoryDepth_NestedPaths(t *testing.T) {
	testCases := []struct {
		path     string
		expected int
	}{
		{"./__test__/", 0},
		{"./__test__/child/", 1},
		{"./__test__/child/grandchild/", 2},
		{"./__test__/a/b/c/d/", 4},
	}

	for _, tc := range testCases {
		result := countTestDirectoryDepth(tc.path)
		if result != tc.expected {
			t.Errorf("countTestDirectoryDepth(%q) = %d, want %d", tc.path, result, tc.expected)
		}
	}
}

func TestFindParentTestDirectory(t *testing.T) {
	dirSet := map[string]bool{
		"./__test__/":                   true,
		"./__test__/child/":             true,
		"./__test__/child/grandchild/":  true,
	}

	testCases := []struct {
		dir      string
		expected *string
	}{
		{"./__test__/", nil},
		{"./__test__/child/", strPtr("./__test__/")},
		{"./__test__/child/grandchild/", strPtr("./__test__/child/")},
	}

	for _, tc := range testCases {
		result := findParentTestDirectory(tc.dir, dirSet)
		if tc.expected == nil {
			if result != nil {
				t.Errorf("findParentTestDirectory(%q) = %q, want nil", tc.dir, *result)
			}
		} else {
			if result == nil {
				t.Errorf("findParentTestDirectory(%q) = nil, want %q", tc.dir, *tc.expected)
			} else if *result != *tc.expected {
				t.Errorf("findParentTestDirectory(%q) = %q, want %q", tc.dir, *result, *tc.expected)
			}
		}
	}
}

func strPtr(s string) *string {
	return &s
}
