package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
)

// Compiled regex patterns for comment removal (compiled once at package init).
var (
	multiLineCommentRegex  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	singleLineCommentRegex = regexp.MustCompile(`--[^\n]*`)
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
//  1. Convert to lowercase
//  2. Remove SQL comments (-- and /* */)
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
	// Step 1: Remove comments (returns new string)
	cleaned := c.removeComments(content)

	// Step 2: Build normalized version with lowercase and collapsed whitespace
	var b strings.Builder
	b.Grow(len(cleaned)) // Pre-allocate to avoid reallocation

	lastWasSpace := false
	for _, r := range cleaned {
		if unicode.IsSpace(r) {
			if !lastWasSpace {
				b.WriteRune(' ')
				lastWasSpace = true
			}
		} else {
			b.WriteRune(unicode.ToLower(r))
			lastWasSpace = false
		}
	}

	return strings.TrimSpace(b.String())
}

// removeComments removes both single-line (--) and multi-line (/* */) SQL comments.
func (c SHA256) removeComments(content string) string {
	// Remove multi-line comments /* ... */ (non-greedy match)
	content = multiLineCommentRegex.ReplaceAllString(content, " ")

	// Remove single-line comments -- ... (to end of line)
	content = singleLineCommentRegex.ReplaceAllString(content, " ")

	return content
}

