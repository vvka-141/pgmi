package metadata

import (
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrNoMetadata = errors.New("no PGMI metadata found")

const (
	MaxMetadataSize = 10 * 1024
)

// blockCommentRegex matches SQL block comments /* ... */
// It uses non-greedy matching to capture the first comment block only.
var blockCommentRegex = regexp.MustCompile(`(?s)/\*\s*(.*?)\s*\*/`)

// metaElementRegex detects the presence of <pgmi-meta tags
var metaElementRegex = regexp.MustCompile(`<\s*pgmi-meta[\s>]`)

// oldMetaElementRegex detects old format <pgmi:meta with namespace
var oldMetaElementRegex = regexp.MustCompile(`<\s*pgmi:meta[\s>]`)

// Extract parses PGMI metadata from the first block comment in SQL file content.
// It searches for /* ... */ comments containing <pgmi-meta> XML elements.
//
// Algorithm:
//  1. Find the first SQL block comment /* ... */
//  2. Check if it contains <pgmi-meta> element
//  3. Parse XML
//  4. Return parsed metadata
//
// Parameters:
//   - content: SQL file content
//   - filePath: File path for error reporting (optional, can be empty)
//
// Returns:
//   - *Metadata: Parsed metadata (nil if no metadata found)
//   - error: ErrNoMetadata if no metadata found, or parsing/validation error
//
// Error cases:
//   - No block comment or no <pgmi-meta> → ErrNoMetadata
//   - Multiple <pgmi-meta> blocks → MetadataError
//   - Invalid XML syntax → wrapped xml.SyntaxError
func Extract(content string, filePath string) (*Metadata, error) {
	// Find all block comments
	matches := blockCommentRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil, ErrNoMetadata
	}

	// Search for metadata in comments (check first few comments)
	var metadataXML string
	var metadataCount int

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}

		commentContent := match[1]

		// Check for old format first (backward compatibility detection)
		if oldMetaElementRegex.MatchString(commentContent) {
			return nil, &MetadataError{
				FilePath: filePath,
				Message:  "Found old metadata format with namespace",
				Hint: "The metadata format changed to remove XML namespaces.\n\n" +
					"Migration required:\n" +
					"  OLD: <pgmi:meta xmlns:pgmi=\"https://pgmi.com/pgmi-metadata/v1\" ...>\n" +
					"  NEW: <pgmi-meta id=\"...\" idempotent=\"...\" sortKey=\"...\">\n\n" +
					"Remove the xmlns:pgmi attribute and change <pgmi:meta> to <pgmi-meta>.",
			}
		}

		// Check if this comment contains <pgmi-meta>
		if metaElementRegex.MatchString(commentContent) {
			metadataCount++
			if metadataCount > 1 {
				return nil, &MetadataError{
					FilePath: filePath,
					Message:  "Multiple metadata blocks found",
					Hint:     "Only one <pgmi-meta> block is allowed per file. Remove duplicates.",
				}
			}
			metadataXML = commentContent
		}
	}

	if metadataCount == 0 {
		return nil, ErrNoMetadata
	}

	// Validate size limit
	if len(metadataXML) > MaxMetadataSize {
		return nil, &MetadataError{
			FilePath: filePath,
			Message:  fmt.Sprintf("Metadata block exceeds maximum size of %d bytes (got %d bytes)", MaxMetadataSize, len(metadataXML)),
			Hint: "Metadata should be concise. Move large descriptions to separate documentation files.\n" +
				"Typical metadata blocks are 200-500 bytes.",
		}
	}

	// Validate content is not empty/whitespace only
	if strings.TrimSpace(metadataXML) == "" {
		return nil, &MetadataError{
			FilePath: filePath,
			Message:  "Metadata block is empty or contains only whitespace",
			Hint:     "The metadata block must contain valid XML with at least id, idempotent, and sortKey attributes.",
		}
	}

	// Parse XML
	var meta Metadata
	decoder := xml.NewDecoder(strings.NewReader(metadataXML))
	if err := decoder.Decode(&meta); err != nil {
		// Use structured error with line numbers if available
		return nil, wrapXMLError(err, filePath)
	}

	return &meta, nil
}

// ExtractAndValidate combines extraction and validation in one call.
// This is a convenience function for the common case.
//
// Returns:
//   - *Metadata: Parsed and validated metadata (nil if no metadata found)
//   - error: ErrNoMetadata (not fatal), or validation/parsing error
func ExtractAndValidate(content string, filePath string) (*Metadata, error) {
	meta, err := Extract(content, filePath)
	if err != nil {
		return nil, err
	}

	// Validate against XSD constraints
	result := Validate(meta, filePath)
	if !result.Valid {
		// Use formatted validation errors with helpful hints
		return nil, formatValidationErrors(result, filePath)
	}

	return meta, nil
}
