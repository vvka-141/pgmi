package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// Calculator is an interface for computing file checksums.
// This abstraction allows for different checksum strategies and algorithms.
type Calculator interface {
	// CalculateRaw computes a checksum of the raw, unmodified content.
	CalculateRaw(content []byte) string

	// CalculateNormalized computes a checksum of normalized content.
	// Normalization makes checksums resilient to formatting changes.
	CalculateNormalized(content []byte) string
}

// SHA256 implements checksum calculation using SHA-256.
// It follows the pgmi normalization strategy:
//  1. Remove SQL comments (-- and /* */) while preserving string literals
//  2. Fold case where SQL itself ignores it — keywords and unquoted
//     identifiers — and nowhere else
//  3. Collapse whitespace to single spaces
//
// SHA256 is a zero-size type and is safe for concurrent use by multiple goroutines.
// Using value semantics (pass by value) eliminates heap allocations.
type SHA256 struct{}

// New creates a new SHA-256 based calculator.
// Returns by value to avoid heap allocation (SHA256 is a zero-size type).
func New() SHA256 {
	return SHA256{}
}

// CalculateRaw computes SHA-256 of raw content.
func (c SHA256) CalculateRaw(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

// CalculateNormalized computes SHA-256 of normalized content.
func (c SHA256) CalculateNormalized(content []byte) string {
	normalized := c.normalize(string(content))
	hash := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(hash[:])
}

// normalize applies the normalization rules to content.
// Uses strings.Builder for efficient string construction to avoid multiple allocations.
func (c SHA256) normalize(content string) string {
	cleaned := c.scan(content, true)

	var b strings.Builder
	b.Grow(len(cleaned))

	lastWasSpace := false
	for _, r := range cleaned {
		if unicode.IsSpace(r) {
			if !lastWasSpace {
				b.WriteRune(' ')
				lastWasSpace = true
			}
		} else {
			b.WriteRune(r)
			lastWasSpace = false
		}
	}

	return strings.TrimSpace(b.String())
}

type commentState int

const (
	csNormal commentState = iota
	csLineComment
	csBlockComment
	csSingleQuote
	csDollarQuote
	csQuotedIdentifier
)

// removeComments removes SQL comments while preserving string literals.
// Handles single-quoted strings (doubled-apostrophe and E” backslash escapes),
// dollar-quoted strings ($$...$$, $tag$...$tag$), quoted identifiers, and
// nested block comments (/* /* */ */).
//
// A comment becomes one space rather than nothing: deleting it outright would
// normalize a/*x*/b to ab, which collides with the different statement `ab`.
// That is why this does not simply call the preprocessor's Strip, whose
// contract is to remove comment bytes entirely.
func (c SHA256) removeComments(content string) string {
	return c.scan(content, false)
}

// scan is that removal, optionally folding case as it goes. Folding belongs
// here, not in a second pass, because only this scan knows which bytes are
// outside a literal. SQL ignores case for keywords and unquoted identifiers
// alone: "Users" and "users" are different tables, and 'Production' and
// 'production' are different data. Folding those made two scripts that deploy
// differently share one checksum, so a change gated on it was skipped.
func (c SHA256) scan(content string, foldCase bool) string {
	var b strings.Builder
	b.Grow(len(content))

	state := csNormal
	blockDepth := 0
	dollarTag := ""
	escapeString := false
	i := 0

	for i < len(content) {
		ch := content[i]
		var next byte
		if i+1 < len(content) {
			next = content[i+1]
		}

		switch state {
		case csNormal:
			if ch == '-' && next == '-' {
				state = csLineComment
				b.WriteByte(' ')
				i += 2
			} else if ch == '/' && next == '*' {
				state = csBlockComment
				blockDepth = 1
				b.WriteByte(' ')
				i += 2
			} else if ch == '\'' {
				state = csSingleQuote
				escapeString = escapeStringPrefixed(content, i)
				b.WriteByte(ch)
				i++
			} else if ch == '"' {
				state = csQuotedIdentifier
				b.WriteByte(ch)
				i++
			} else if ch == '$' {
				tag := extractDollarTag(content, i)
				if tag != "" {
					state = csDollarQuote
					dollarTag = tag
					b.WriteString(tag)
					i += len(tag)
				} else {
					b.WriteByte(ch)
					i++
				}
			} else {
				if foldCase {
					ch = foldASCII(ch)
				}
				b.WriteByte(ch)
				i++
			}

		case csLineComment:
			if ch == '\n' {
				b.WriteByte(ch)
				state = csNormal
				i++
			} else if ch == '\r' && next == '\n' {
				b.WriteByte(ch)
				b.WriteByte(next)
				state = csNormal
				i += 2
			} else {
				i++
			}

		case csBlockComment:
			if ch == '/' && next == '*' {
				blockDepth++
				i += 2
			} else if ch == '*' && next == '/' {
				blockDepth--
				i += 2
				if blockDepth == 0 {
					state = csNormal
				}
			} else {
				i++
			}

		case csSingleQuote:
			if escapeString && ch == '\\' && i+1 < len(content) {
				// Only E'...' lets a backslash escape the next byte. Without
				// this, E'a\'-- x' ended at the escaped quote and the rest of
				// the line vanished as a comment, so two different statements
				// normalized to the same bytes.
				b.WriteByte(ch)
				b.WriteByte(next)
				i += 2
				continue
			}
			b.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' {
					b.WriteByte(next)
					i += 2
				} else {
					state = csNormal
					i++
				}
			} else {
				i++
			}

		case csQuotedIdentifier:
			b.WriteByte(ch)
			if ch == '"' {
				if next == '"' {
					b.WriteByte(next)
					i += 2
				} else {
					state = csNormal
					i++
				}
			} else {
				i++
			}

		case csDollarQuote:
			if matchesTag(content, i, dollarTag) {
				b.WriteString(dollarTag)
				i += len(dollarTag)
				state = csNormal
				dollarTag = ""
			} else {
				b.WriteByte(ch)
				i++
			}
		}
	}

	return b.String()
}

// extractDollarTag extracts a dollar-quote tag (e.g., "$$" or "$tag$") starting at position i.
// Returns empty string if not a valid dollar-quote tag.
func extractDollarTag(s string, i int) string {
	if i >= len(s) || s[i] != '$' {
		return ""
	}

	j := i + 1
	for j < len(s) {
		ch := s[j]
		if ch == '$' {
			return s[i : j+1]
		}
		if j == i+1 {
			if !isTagStart(ch) {
				return ""
			}
		} else {
			if !isTagContinue(ch) {
				return ""
			}
		}
		j++
	}

	return ""
}

// escapeStringPrefixed reports whether the quote at content[i] opens an E'...'
// escape string. With standard_conforming_strings on — the default since 9.1 —
// a backslash is literal in an ordinary '...' literal. The E must be a whole
// token: in abcE'x' the lexer reads the identifier abcE, so the backslash
// stays literal. Mirrors the preprocessor's rune-based version.
func escapeStringPrefixed(content string, i int) bool {
	if i == 0 || (content[i-1] != 'E' && content[i-1] != 'e') {
		return false
	}
	return i == 1 || !isIdentifierByte(content[i-2])
}

// foldASCII lowercases an ASCII letter. Bytes above 0x7F are left alone: they
// are UTF-8 continuation bytes here, and folding one in isolation would corrupt
// the character it belongs to.
func foldASCII(ch byte) byte {
	if ch >= 'A' && ch <= 'Z' {
		return ch + ('a' - 'A')
	}
	return ch
}

func isIdentifierByte(ch byte) bool {
	return ch == '_' || ch == '$' || ch >= 0x80 ||
		(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
}

func isTagStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isTagContinue(ch byte) bool {
	return isTagStart(ch) || (ch >= '0' && ch <= '9')
}

// matchesTag checks if the string at position i starts with the given tag.
func matchesTag(s string, i int, tag string) bool {
	if i+len(tag) > len(s) {
		return false
	}
	return s[i:i+len(tag)] == tag
}
