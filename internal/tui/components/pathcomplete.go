package components

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PathCompleter provides tab-completion and cycling for filesystem paths.
// It tracks state across Tab presses to cycle through matches.
//
// Usage:
//
//	completer := NewPathCompleter(true) // dirs only
//
//	// On Tab press:
//	completed := completer.Next(input.Value())
//	input.SetValue(completed)
//
//	// On any other keypress:
//	completer.Reset()
type PathCompleter struct {
	matches    []string
	cycleIndex int
	lastInput  string
	dirsOnly   bool
}

// NewPathCompleter creates a new path completer.
// If dirsOnly is true, only directories are matched.
func NewPathCompleter(dirsOnly bool) *PathCompleter {
	return &PathCompleter{dirsOnly: dirsOnly}
}

// Next returns the next completion for the given input.
// On first call (or after input changes), it computes matches.
// On subsequent calls with the same base input, it cycles through matches.
func (c *PathCompleter) Next(input string) string {
	parent, prefix := splitPath(input)

	// If the input changed from what we're cycling through, recompute
	if parent != c.lastInput || c.matches == nil {
		c.matches = c.findMatches(parent, prefix)
		c.cycleIndex = 0
		c.lastInput = parent

		if len(c.matches) == 0 {
			return input
		}

		// First Tab: if there's a unique common prefix longer than input, complete it
		if len(c.matches) > 1 {
			common := longestCommonPrefix(c.matches)
			candidate := filepath.Join(parent, common)
			if len(candidate) > len(input) {
				return candidate
			}
		}

		// Single match or common prefix exhausted — return first match
		return c.formatMatch(parent, c.matches[c.cycleIndex])
	}

	// Same parent — cycle to next match
	if len(c.matches) == 0 {
		return input
	}

	c.cycleIndex = (c.cycleIndex + 1) % len(c.matches)
	return c.formatMatch(parent, c.matches[c.cycleIndex])
}

// Reset clears the cycle state. Call this when the user types a non-Tab key.
func (c *PathCompleter) Reset() {
	c.matches = nil
	c.cycleIndex = 0
	c.lastInput = ""
}

func (c *PathCompleter) findMatches(parent, prefix string) []string {
	if parent == "" {
		parent = "."
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}

	var matches []string
	lowPrefix := strings.ToLower(prefix)

	for _, entry := range entries {
		if c.dirsOnly && !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(strings.ToLower(name), lowPrefix) {
			matches = append(matches, name)
		}
	}

	sort.Strings(matches)
	return matches
}

func (c *PathCompleter) formatMatch(parent, name string) string {
	result := filepath.Join(parent, name)

	// Check if the match is a directory — add trailing separator
	info, err := os.Stat(result)
	if err == nil && info.IsDir() {
		result += string(filepath.Separator)
	}

	return result
}

// splitPath splits an input into parent directory and name prefix.
//
//	"./src/com" → ("src", "com")
//	"./src/"    → ("./src", "")
//	"my"        → (".", "my")
//	""          → (".", "")
//	"."         → (".", "")
func splitPath(input string) (parent, prefix string) {
	if input == "" || input == "." {
		return ".", ""
	}

	if strings.HasSuffix(input, string(filepath.Separator)) || strings.HasSuffix(input, "/") {
		return strings.TrimRight(input, `/\`), ""
	}

	parent = filepath.Dir(input)
	prefix = filepath.Base(input)
	return parent, prefix
}

// longestCommonPrefix finds the longest common prefix among strings (case-insensitive).
func longestCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	if len(strs) == 1 {
		return strs[0]
	}

	lowered := make([]string, len(strs))
	for i, s := range strs {
		lowered[i] = strings.ToLower(s)
	}

	first := lowered[0]
	rest := lowered[1:]
	for i := 0; i < len(first); i++ {
		ch := first[i]
		for _, s := range rest {
			if i >= len(s) || s[i] != ch {
				return strs[0][:i]
			}
		}
	}
	return strs[0]
}
