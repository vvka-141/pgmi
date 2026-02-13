package preprocessor

import (
	"strings"
	"unicode"
)

// LineMapping maps a line in the stripped output to its original position.
type LineMapping struct {
	StrippedLine int
	OriginalLine int
	OriginalCol  int
}

// CommentStripper removes SQL comments while preserving string literals.
type CommentStripper interface {
	Strip(sql string) (stripped string, lineMap []LineMapping)
}

// commentStripper implements CommentStripper using a state machine.
type commentStripper struct{}

// NewCommentStripper creates a new CommentStripper instance.
func NewCommentStripper() CommentStripper {
	return &commentStripper{}
}

// parserState represents the current state of the parser.
type parserState int

const (
	stateNormal parserState = iota
	stateLineComment
	stateBlockComment
	stateSingleQuote
	stateDollarQuote
)

// Strip removes SQL comments and returns the stripped SQL with line mapping.
// Handles:
// - Single-line comments: -- to end of line
// - Block comments: /* */ with PostgreSQL nesting support
// - Single-quoted strings: '...' with '' escape
// - Dollar-quoted strings: $$...$$ and $tag$...$tag$
func (c *commentStripper) Strip(sql string) (string, []LineMapping) {
	if len(sql) == 0 {
		return "", nil
	}

	var result strings.Builder
	result.Grow(len(sql))

	var lineMap []LineMapping
	state := stateNormal
	blockDepth := 0
	dollarTag := ""

	originalLine := 1
	originalCol := 1
	strippedLine := 1

	runes := []rune(sql)
	i := 0

	for i < len(runes) {
		r := runes[i]
		var next rune
		if i+1 < len(runes) {
			next = runes[i+1]
		}

		switch state {
		case stateNormal:
			if r == '-' && next == '-' {
				// Start of line comment
				state = stateLineComment
				i += 2
				originalCol += 2
			} else if r == '/' && next == '*' {
				// Start of block comment
				state = stateBlockComment
				blockDepth = 1
				i += 2
				originalCol += 2
			} else if r == '\'' {
				// Start of single-quoted string
				state = stateSingleQuote
				result.WriteRune(r)
				i++
				originalCol++
			} else if r == '$' {
				// Possible start of dollar-quoted string
				tag := c.extractDollarTag(runes, i)
				if tag != "" {
					state = stateDollarQuote
					dollarTag = tag
					result.WriteString(tag)
					i += len(tag)
					originalCol += len(tag)
				} else {
					result.WriteRune(r)
					i++
					originalCol++
				}
			} else {
				result.WriteRune(r)
				if r == '\n' {
					lineMap = append(lineMap, LineMapping{
						StrippedLine: strippedLine,
						OriginalLine: originalLine,
						OriginalCol:  originalCol,
					})
					originalLine++
					originalCol = 1
					strippedLine++
				} else {
					originalCol++
				}
				i++
			}

		case stateLineComment:
			if r == '\n' {
				// End of line comment, preserve the newline
				result.WriteRune(r)
				lineMap = append(lineMap, LineMapping{
					StrippedLine: strippedLine,
					OriginalLine: originalLine,
					OriginalCol:  originalCol,
				})
				originalLine++
				originalCol = 1
				strippedLine++
				state = stateNormal
				i++
			} else if r == '\r' && next == '\n' {
				// Windows line ending - preserve both \r\n
				result.WriteRune(r)
				result.WriteRune(next)
				lineMap = append(lineMap, LineMapping{
					StrippedLine: strippedLine,
					OriginalLine: originalLine,
					OriginalCol:  originalCol,
				})
				originalLine++
				originalCol = 1
				strippedLine++
				state = stateNormal
				i += 2
			} else {
				originalCol++
				i++
			}

		case stateBlockComment:
			if r == '/' && next == '*' {
				// Nested block comment start
				blockDepth++
				i += 2
				originalCol += 2
			} else if r == '*' && next == '/' {
				// Block comment end
				blockDepth--
				i += 2
				originalCol += 2
				if blockDepth == 0 {
					state = stateNormal
				}
			} else {
				if r == '\n' {
					originalLine++
					originalCol = 1
				} else {
					originalCol++
				}
				i++
			}

		case stateSingleQuote:
			result.WriteRune(r)
			if r == '\'' {
				// Check for escaped quote ''
				if next == '\'' {
					result.WriteRune(next)
					i += 2
					originalCol += 2
				} else {
					// End of string
					state = stateNormal
					i++
					originalCol++
				}
			} else {
				if r == '\n' {
					lineMap = append(lineMap, LineMapping{
						StrippedLine: strippedLine,
						OriginalLine: originalLine,
						OriginalCol:  originalCol,
					})
					originalLine++
					originalCol = 1
					strippedLine++
				} else {
					originalCol++
				}
				i++
			}

		case stateDollarQuote:
			// Look for closing dollar tag
			if c.matchesDollarTag(runes, i, dollarTag) {
				result.WriteString(dollarTag)
				i += len(dollarTag)
				originalCol += len(dollarTag)
				state = stateNormal
				dollarTag = ""
			} else {
				result.WriteRune(r)
				if r == '\n' {
					lineMap = append(lineMap, LineMapping{
						StrippedLine: strippedLine,
						OriginalLine: originalLine,
						OriginalCol:  originalCol,
					})
					originalLine++
					originalCol = 1
					strippedLine++
				} else {
					originalCol++
				}
				i++
			}
		}
	}

	// Add final line mapping if not already added
	if len(lineMap) == 0 || lineMap[len(lineMap)-1].StrippedLine != strippedLine {
		lineMap = append(lineMap, LineMapping{
			StrippedLine: strippedLine,
			OriginalLine: originalLine,
			OriginalCol:  originalCol,
		})
	}

	return result.String(), lineMap
}

// extractDollarTag extracts a dollar-quote tag starting at position i.
// Returns the full tag (e.g., "$$" or "$tag$") or empty string if not a valid tag.
func (c *commentStripper) extractDollarTag(runes []rune, i int) string {
	if i >= len(runes) || runes[i] != '$' {
		return ""
	}

	// Look for closing $
	j := i + 1
	for j < len(runes) {
		r := runes[j]
		if r == '$' {
			// Found closing $, extract tag
			return string(runes[i : j+1])
		}
		// Tag can contain letters, digits, and underscores (but not start with digit)
		if j == i+1 {
			// First char after $ must be letter, underscore, or $ itself
			if !unicode.IsLetter(r) && r != '_' && r != '$' {
				// Check if it could be a number (like $1)
				if unicode.IsDigit(r) {
					// Continue, might be $1$
				} else {
					return ""
				}
			}
		} else {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
				return ""
			}
		}
		j++
	}

	return ""
}

// matchesDollarTag checks if the runes starting at position i match the given dollar tag.
func (c *commentStripper) matchesDollarTag(runes []rune, i int, tag string) bool {
	tagRunes := []rune(tag)
	if i+len(tagRunes) > len(runes) {
		return false
	}

	for j, tr := range tagRunes {
		if runes[i+j] != tr {
			return false
		}
	}
	return true
}
