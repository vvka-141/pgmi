package preprocessor

import (
	"strings"
	"unicode"
)

// CommentStripper removes SQL comments while preserving string literals.
type CommentStripper interface {
	Strip(sql string) string
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

// Strip removes SQL comments and returns the stripped SQL.
// Handles:
// - Single-line comments: -- to end of line
// - Block comments: /* */ with PostgreSQL nesting support
// - Single-quoted strings: '...' with '' escape
// - Dollar-quoted strings: $$...$$ and $tag$...$tag$
func (c *commentStripper) Strip(sql string) string {
	if len(sql) == 0 {
		return ""
	}

	var result strings.Builder
	result.Grow(len(sql))

	state := stateNormal
	blockDepth := 0
	dollarTag := ""

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
				state = stateLineComment
				i += 2
			} else if r == '/' && next == '*' {
				state = stateBlockComment
				blockDepth = 1
				i += 2
			} else if r == '\'' {
				state = stateSingleQuote
				result.WriteRune(r)
				i++
			} else if r == '$' {
				tag := c.extractDollarTag(runes, i)
				if tag != "" {
					state = stateDollarQuote
					dollarTag = tag
					result.WriteString(tag)
					i += len(tag)
				} else {
					result.WriteRune(r)
					i++
				}
			} else {
				result.WriteRune(r)
				i++
			}

		case stateLineComment:
			if r == '\n' {
				result.WriteRune(r)
				state = stateNormal
				i++
			} else if r == '\r' && next == '\n' {
				result.WriteRune(r)
				result.WriteRune(next)
				state = stateNormal
				i += 2
			} else {
				i++
			}

		case stateBlockComment:
			if r == '/' && next == '*' {
				blockDepth++
				i += 2
			} else if r == '*' && next == '/' {
				blockDepth--
				i += 2
				if blockDepth == 0 {
					state = stateNormal
				}
			} else {
				i++
			}

		case stateSingleQuote:
			result.WriteRune(r)
			if r == '\'' {
				if next == '\'' {
					result.WriteRune(next)
					i += 2
				} else {
					state = stateNormal
					i++
				}
			} else {
				i++
			}

		case stateDollarQuote:
			if c.matchesDollarTag(runes, i, dollarTag) {
				result.WriteString(dollarTag)
				i += len(dollarTag)
				state = stateNormal
				dollarTag = ""
			} else {
				result.WriteRune(r)
				i++
			}
		}
	}

	return result.String()
}

// extractDollarTag extracts a dollar-quote tag starting at position i.
// Returns the full tag (e.g., "$$" or "$tag$") or empty string if not a valid tag.
func (c *commentStripper) extractDollarTag(runes []rune, i int) string {
	if i >= len(runes) || runes[i] != '$' {
		return ""
	}

	j := i + 1
	for j < len(runes) {
		r := runes[j]
		if r == '$' {
			return string(runes[i : j+1])
		}
		if j == i+1 {
			// Tag identifier must start with letter or underscore (PostgreSQL spec)
			if !unicode.IsLetter(r) && r != '_' {
				return ""
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
