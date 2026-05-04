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
	// Detect finds macro calls. The `sql` argument is the original text
	// (used to extract Pattern/Callback substrings). The `mask` argument is
	// a length-preserved redacted copy where comment/string bytes are
	// replaced with spaces (see CommentStripper.RedactForMacros) — the
	// regex runs over mask so it cannot match inside literals or comments.
	// Byte offsets in the returned MacroCall are positions in `sql`.
	Detect(sql string, mask string) []MacroCall
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

// Detect finds all pgmi macro calls. See interface doc for the two-argument
// contract. For legacy callers that have already masked their input, pass the
// same string for both arguments.
func (d *macroDetector) Detect(sql string, mask string) []MacroCall {
	if mask == "" {
		mask = sql
	}
	// Sanity: mask MUST align byte-for-byte with sql. RedactForMacros
	// guarantees this; if a caller passes something shorter, positions from
	// the match would index out of range on `sql`.
	if len(mask) != len(sql) {
		// Fall back to single-source behaviour when misaligned.
		mask = sql
	}

	matches := d.pattern.FindAllStringSubmatchIndex(mask, -1)
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
		// (the [^a-zA-Z0-9_] part of the pattern). The boundary byte is the
		// same in sql and mask (it is whitespace/punctuation outside of
		// strings), so this works on either source.
		if startPos < len(mask) {
			firstChar := rune(mask[startPos])
			if firstChar != 'c' && firstChar != 'C' {
				startPos++
			}
		}

		// Extract pattern/callback from the ORIGINAL sql — the mask has them
		// replaced with spaces since they live inside single-quoted literals.
		pattern := ""
		if match[2] != -1 && match[3] != -1 {
			pattern = sql[match[2]:match[3]]
		}

		callback := ""
		if match[4] != -1 && match[5] != -1 {
			callback = sql[match[4]:match[5]]
		}

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
