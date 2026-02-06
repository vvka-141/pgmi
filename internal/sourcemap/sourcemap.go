// Package sourcemap provides source mapping for error attribution.
package sourcemap

// Entry maps a range of lines in expanded SQL to their original source.
type Entry struct {
	ExpandedStart int    // First line in expanded SQL (1-based, inclusive)
	ExpandedEnd   int    // Last line in expanded SQL (1-based, inclusive)
	OriginalFile  string // Original source file path
	OriginalLine  int    // Line number in original file
	Description   string // Human-readable description (e.g., "test: ./users/__test__/01_test.sql")
}

// SourceMap tracks how lines in expanded SQL map back to original sources.
// Used for error attribution when macro expansion inserts generated code.
type SourceMap struct {
	entries []Entry
}

// New creates a new empty SourceMap.
func New() *SourceMap {
	return &SourceMap{
		entries: make([]Entry, 0),
	}
}

// Add records a mapping from expanded line range to original source.
// Lines are 1-based and inclusive on both ends.
func (sm *SourceMap) Add(expandedStart, expandedEnd int, file string, line int, desc string) {
	sm.entries = append(sm.entries, Entry{
		ExpandedStart: expandedStart,
		ExpandedEnd:   expandedEnd,
		OriginalFile:  file,
		OriginalLine:  line,
		Description:   desc,
	})
}

// Merge incorporates another SourceMap's entries with a line offset.
// The offset is added to all expanded line numbers in the other map.
// This is used when inserting macro expansions at a specific line.
func (sm *SourceMap) Merge(other *SourceMap, lineOffset int) {
	if other == nil {
		return
	}
	for _, entry := range other.entries {
		sm.entries = append(sm.entries, Entry{
			ExpandedStart: entry.ExpandedStart + lineOffset,
			ExpandedEnd:   entry.ExpandedEnd + lineOffset,
			OriginalFile:  entry.OriginalFile,
			OriginalLine:  entry.OriginalLine,
			Description:   entry.Description,
		})
	}
}

// Resolve finds the original source for an expanded line number.
// Returns the file, line, description, and whether a mapping was found.
func (sm *SourceMap) Resolve(expandedLine int) (file string, line int, desc string, found bool) {
	// Linear search - could be optimized with binary search if needed
	for _, entry := range sm.entries {
		if expandedLine >= entry.ExpandedStart && expandedLine <= entry.ExpandedEnd {
			return entry.OriginalFile, entry.OriginalLine, entry.Description, true
		}
	}
	return "", 0, "", false
}

// Entries returns a copy of all source entries.
func (sm *SourceMap) Entries() []Entry {
	result := make([]Entry, len(sm.entries))
	copy(result, sm.entries)
	return result
}

// Len returns the number of entries in the source map.
func (sm *SourceMap) Len() int {
	return len(sm.entries)
}
