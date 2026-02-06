package testdiscovery

import (
	"path/filepath"
	"strings"
)

// PatternMatcher implements glob pattern matching for test paths.
type PatternMatcher struct{}

// NewPatternMatcher creates a new PatternMatcher.
func NewPatternMatcher() *PatternMatcher {
	return &PatternMatcher{}
}

// Matches checks if a path matches a glob pattern.
// Supports:
// - * matches any sequence of non-separator characters (within one segment)
// - ** matches zero or more directories
// - ? matches any single non-separator character
// - Empty pattern matches everything
func (m *PatternMatcher) Matches(pattern, path string) bool {
	if pattern == "" {
		return true
	}

	// Normalize separators
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	// Handle ** (recursive glob)
	if strings.Contains(pattern, "**") {
		return m.matchDoubleStar(pattern, path)
	}

	// Match segment by segment to ensure * doesn't cross boundaries
	return m.matchSegments(pattern, path)
}

// matchSegments matches pattern and path segment by segment.
func (m *PatternMatcher) matchSegments(pattern, path string) bool {
	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")

	if len(patternParts) != len(pathParts) {
		return false
	}

	for i := range patternParts {
		matched, err := filepath.Match(patternParts[i], pathParts[i])
		if err != nil || !matched {
			return false
		}
	}
	return true
}

// matchDoubleStar handles patterns with ** (recursive matching).
func (m *PatternMatcher) matchDoubleStar(pattern, path string) bool {
	// Split pattern by **
	parts := strings.Split(pattern, "**")

	if len(parts) == 2 && parts[1] == "" {
		// Pattern ends with ** (e.g., "./users/**")
		prefix := strings.TrimSuffix(parts[0], "/")
		if prefix == "" {
			return true
		}
		// Path must start with prefix or equal prefix
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}

	// Pattern has ** in the middle (e.g., "./users/**/__test__")
	return m.matchDoubleStarRecursive(parts, path)
}

// matchDoubleStarRecursive handles ** in the middle of patterns.
func (m *PatternMatcher) matchDoubleStarRecursive(parts []string, path string) bool {
	if len(parts) == 0 {
		return true
	}

	prefix := strings.TrimSuffix(parts[0], "/")
	suffix := strings.TrimPrefix(parts[len(parts)-1], "/")

	// Check prefix
	if prefix != "" {
		if path == prefix {
			// Exact match to prefix, suffix must be empty for ** to match zero segments
			return suffix == ""
		}
		if !strings.HasPrefix(path, prefix+"/") {
			return false
		}
		path = path[len(prefix)+1:]
	}

	// Check suffix
	if suffix != "" {
		// Try matching suffix at any depth
		if strings.HasSuffix(path, "/"+suffix) {
			return true
		}
		if path == suffix {
			return true
		}
		// Try matching with glob in suffix
		pathParts := strings.Split(path, "/")
		for i := range pathParts {
			candidate := strings.Join(pathParts[i:], "/")
			matched, _ := filepath.Match(suffix, candidate)
			if matched {
				return true
			}
		}
		return false
	}

	return true
}

// FilterByPattern filters TestScriptRows to only include rows matching the pattern.
// It filters by Directory field and preserves savepoint/cleanup structure.
func FilterByPattern(rows []TestScriptRow, pattern string) []TestScriptRow {
	if pattern == "" {
		return rows
	}

	matcher := NewPatternMatcher()

	// First, determine which directories match
	matchingDirs := make(map[string]bool)
	for _, row := range rows {
		if row.Directory != "" && matcher.Matches(pattern, row.Directory) {
			matchingDirs[row.Directory] = true
		}
		// Also check path for test/fixture rows
		if row.ScriptPath != nil && matcher.Matches(pattern, *row.ScriptPath) {
			matchingDirs[row.Directory] = true
		}
	}

	// Filter rows to only include matching directories
	var result []TestScriptRow
	for _, row := range rows {
		if matchingDirs[row.Directory] {
			result = append(result, row)
		}
	}

	return result
}
