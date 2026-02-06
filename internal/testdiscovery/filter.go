package testdiscovery

// FilterByPattern returns a new TestTree containing only tests that match the pattern.
// Ancestor directories with their fixtures are preserved for matched tests.
// Empty pattern returns a copy of the full tree.
func (t *TestTree) FilterByPattern(pattern string) *TestTree {
	if t == nil || t.IsEmpty() {
		return NewTestTree()
	}

	// Empty pattern means no filtering
	if pattern == "" {
		return t.deepCopy()
	}

	matcher := NewPatternMatcher()
	result := NewTestTree()

	for _, dir := range t.Directories {
		if filtered := filterDirectory(dir, pattern, matcher); filtered != nil {
			result.AddDirectory(filtered)
		}
	}

	return result
}

// filterDirectory recursively filters a directory and its children.
// Returns nil if no tests match in this directory or its descendants.
func filterDirectory(dir *TestDirectory, pattern string, matcher *PatternMatcher) *TestDirectory {
	// First, check if any tests in this directory match
	var matchingTests []*TestFile
	for _, test := range dir.Tests {
		if matchesPattern(test.Path, pattern, matcher) {
			matchingTests = append(matchingTests, test)
		}
	}

	// Then, recursively filter children
	var matchingChildren []*TestDirectory
	for _, child := range dir.Children {
		if filtered := filterDirectory(child, pattern, matcher); filtered != nil {
			matchingChildren = append(matchingChildren, filtered)
		}
	}

	// If no tests match and no children have matches, exclude this directory
	if len(matchingTests) == 0 && len(matchingChildren) == 0 {
		return nil
	}

	// Create filtered directory with matching tests and children
	filtered := NewTestDirectory(dir.Path, dir.Depth)

	// Preserve fixture if any tests or children match (ancestor fixture)
	if dir.HasFixture() {
		filtered.SetFixture(dir.Fixture)
	}

	// Add only matching tests
	for _, test := range matchingTests {
		filtered.AddTest(test)
	}

	// Add filtered children
	for _, child := range matchingChildren {
		filtered.AddChild(child)
	}

	return filtered
}

// matchesPattern checks if a path matches the filter pattern.
// Supports glob patterns and exact matches.
func matchesPattern(path, pattern string, matcher *PatternMatcher) bool {
	// Exact match
	if path == pattern {
		return true
	}

	// Glob pattern match
	return matcher.Matches(pattern, path)
}

// deepCopy creates a deep copy of the TestTree.
func (t *TestTree) deepCopy() *TestTree {
	if t == nil {
		return nil
	}

	copy := NewTestTree()
	for _, dir := range t.Directories {
		copy.AddDirectory(copyDirectory(dir))
	}
	return copy
}

// copyDirectory creates a deep copy of a TestDirectory.
func copyDirectory(dir *TestDirectory) *TestDirectory {
	if dir == nil {
		return nil
	}

	copy := NewTestDirectory(dir.Path, dir.Depth)

	if dir.HasFixture() {
		copy.SetFixture(dir.Fixture)
	}

	for _, test := range dir.Tests {
		copy.AddTest(test)
	}

	for _, child := range dir.Children {
		copy.AddChild(copyDirectory(child))
	}

	return copy
}
