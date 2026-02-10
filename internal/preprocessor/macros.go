package preprocessor

import (
	"regexp"
)

// MacroCall represents a detected macro invocation in SQL.
type MacroCall struct {
	Name     string // Always "pgmi_test"
	Pattern  string // Glob pattern argument, empty if NULL or no arg
	Callback string // Callback function name, empty if not specified
	StartPos int    // Byte offset in input (inclusive)
	EndPos   int    // Byte offset in input (exclusive)
	Line     int    // 1-based line number
	Column   int    // 1-based column number
}

// MacroDetector detects pgmi macro calls in SQL.
type MacroDetector interface {
	Detect(sql string) []MacroCall
}

// macroDetector implements MacroDetector using regex.
type macroDetector struct {
	pattern *regexp.Regexp
}

// NewMacroDetector creates a new MacroDetector instance.
// The detector expects comment-stripped SQL input.
func NewMacroDetector() MacroDetector {
	// Pattern matches CALL pgmi_test() syntax only:
	// - Word boundary (not preceded by alphanumeric or underscore)
	// - CALL prefix (required)
	// - Optional pg_temp. prefix
	// - pgmi_test
	// - Parentheses with optional whitespace
	// - Optional first argument: NULL, empty, or 'pattern'
	// - Optional second argument: callback function name
	// - Optional trailing semicolon
	pattern := regexp.MustCompile(
		`(?i)(?:^|[^a-zA-Z0-9_])CALL\s+(?:pg_temp\.)?pgmi_test\s*\(\s*(?:'([^']*)'|NULL)?(?:\s*,\s*'([^']*)')?\s*\)\s*;?`,
	)
	return &macroDetector{pattern: pattern}
}

// Detect finds all pgmi macro calls in the given SQL string.
// The input should be comment-stripped SQL.
// Returns macros in order of appearance.
func (d *macroDetector) Detect(sql string) []MacroCall {
	matches := d.pattern.FindAllStringSubmatchIndex(sql, -1)
	if len(matches) == 0 {
		return nil
	}

	macros := make([]MacroCall, 0, len(matches))

	for _, match := range matches {
		// match[0:2] = full match start:end
		// match[2:4] = capture group 1 (pattern) start:end, -1 if not matched
		// match[4:6] = capture group 2 (callback) start:end, -1 if not matched

		startPos := match[0]
		endPos := match[1]

		// Adjust start position if we matched a word boundary character
		// (the [^a-zA-Z0-9_] part of the pattern)
		// We need to skip the boundary char but keep CALL prefix
		if startPos < len(sql) {
			matchedText := sql[startPos:endPos]
			if len(matchedText) > 0 {
				firstChar := rune(matchedText[0])
				// Skip if it's not 'c' or 'C' (CALL)
				if firstChar != 'c' && firstChar != 'C' {
					startPos++
				}
			}
		}

		// Extract pattern if present
		pattern := ""
		if match[2] != -1 && match[3] != -1 {
			pattern = sql[match[2]:match[3]]
		}

		// Extract callback if present
		callback := ""
		if match[4] != -1 && match[5] != -1 {
			callback = sql[match[4]:match[5]]
		}

		// Calculate line and column
		line, column := d.calculatePosition(sql, startPos)

		macros = append(macros, MacroCall{
			Name:     "pgmi_test",
			Pattern:  pattern,
			Callback: callback,
			StartPos: startPos,
			EndPos:   endPos,
			Line:     line,
			Column:   column,
		})
	}

	return macros
}

// calculatePosition returns the 1-based line and column for a byte offset.
func (d *macroDetector) calculatePosition(sql string, offset int) (line, column int) {
	line = 1
	column = 1

	for i := 0; i < offset && i < len(sql); i++ {
		if sql[i] == '\n' {
			line++
			column = 1
		} else {
			column++
		}
	}

	return line, column
}
