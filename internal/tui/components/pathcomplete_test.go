package components

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathCompleter_SingleMatch(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "migrations"), 0755)
	os.Mkdir(filepath.Join(dir, "scripts"), 0755)

	c := NewPathCompleter(true)
	result := c.Next(filepath.Join(dir, "mig"))

	if !strings.Contains(result, "migrations") {
		t.Errorf("expected completion to contain 'migrations', got: %s", result)
	}
	if !strings.HasSuffix(result, string(filepath.Separator)) {
		t.Errorf("expected trailing separator, got: %s", result)
	}
}

func TestPathCompleter_CyclesThroughMatches(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "alpha"), 0755)
	os.Mkdir(filepath.Join(dir, "beta"), 0755)
	os.Mkdir(filepath.Join(dir, "gamma"), 0755)

	c := NewPathCompleter(true)

	// First Tab — gets first match
	r1 := c.Next(dir + string(filepath.Separator))
	// Second Tab — cycles to next
	r2 := c.Next(dir + string(filepath.Separator))
	// Third Tab — cycles to next
	r3 := c.Next(dir + string(filepath.Separator))

	results := []string{r1, r2, r3}

	// All three should be different
	if r1 == r2 || r2 == r3 {
		t.Errorf("expected cycling through matches, got: %v", results)
	}

	// All should contain the dir
	for _, r := range results {
		if !strings.HasPrefix(r, dir) {
			t.Errorf("expected result to start with %s, got: %s", dir, r)
		}
	}
}

func TestPathCompleter_ResetStopsCycling(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "alpha"), 0755)
	os.Mkdir(filepath.Join(dir, "beta"), 0755)

	c := NewPathCompleter(true)

	r1 := c.Next(dir + string(filepath.Separator))
	c.Reset()
	r2 := c.Next(dir + string(filepath.Separator))

	// After reset, should start from the beginning again
	if r1 != r2 {
		t.Errorf("expected same result after reset, got: %s vs %s", r1, r2)
	}
}

func TestPathCompleter_DirsOnly(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0644)

	c := NewPathCompleter(true)
	result := c.Next(dir + string(filepath.Separator))

	if !strings.Contains(result, "subdir") {
		t.Errorf("expected dir match 'subdir', got: %s", result)
	}
}

func TestPathCompleter_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	c := NewPathCompleter(true)
	result := c.Next(dir + string(filepath.Separator))

	// No subdirs — should return input unchanged
	if result != dir+string(filepath.Separator) {
		t.Errorf("expected unchanged input for empty dir, got: %s", result)
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		input          string
		expectedParent string
		expectedPrefix string
	}{
		{"", ".", ""},
		{".", ".", ""},
		{"my", ".", "my"},
	}

	for _, tt := range tests {
		parent, prefix := splitPath(tt.input)
		if parent != tt.expectedParent || prefix != tt.expectedPrefix {
			t.Errorf("splitPath(%q) = (%q, %q), want (%q, %q)",
				tt.input, parent, prefix, tt.expectedParent, tt.expectedPrefix)
		}
	}
}
