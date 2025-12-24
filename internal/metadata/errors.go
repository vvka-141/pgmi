package metadata

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// MetadataError represents a structured error with context and helpful hints.
// It includes file path, optional line/column numbers, and actionable suggestions.
type MetadataError struct {
	FilePath string // Path to the file with the error
	Line     int    // Line number (0 if unknown)
	Column   int    // Column number (0 if unknown)
	Field    string // Field name (e.g., "id", "sortKey") if applicable
	Message  string // Primary error message
	Hint     string // Actionable suggestion for fixing
}

// Error implements the error interface with rich formatting.
func (e *MetadataError) Error() string {
	var location string
	if e.Line > 0 {
		if e.Column > 0 {
			location = fmt.Sprintf("%s (line %d, col %d)", e.FilePath, e.Line, e.Column)
		} else {
			location = fmt.Sprintf("%s (line %d)", e.FilePath, e.Line)
		}
	} else {
		location = e.FilePath
	}

	msg := fmt.Sprintf("metadata error in %s: %s", location, e.Message)

	if e.Field != "" {
		msg = fmt.Sprintf("metadata error in %s [field: %s]: %s", location, e.Field, e.Message)
	}

	if e.Hint != "" {
		msg += "\n\nHint: " + e.Hint
	}

	return msg
}

// wrapXMLError converts xml package errors to MetadataError with line numbers.
func wrapXMLError(err error, filePath string) error {
	if syntaxErr, ok := err.(*xml.SyntaxError); ok {
		return &MetadataError{
			FilePath: filePath,
			Line:     int(syntaxErr.Line),
			Message:  syntaxErr.Msg,
			Hint: "Check that all XML tags are properly closed and attributes are quoted.\n\n" +
				"Expected format:\n" +
				"  <pgmi-meta id=\"UUID\" idempotent=\"true|false\" sortKey=\"...\">\n" +
				"    <description>...</description>\n" +
				"  </pgmi-meta>",
		}
	}

	// Generic XML unmarshaling error
	return &MetadataError{
		FilePath: filePath,
		Message:  err.Error(),
		Hint: "Verify the metadata XML structure matches the expected format.\n" +
			"See: internal/metadata/schema.xsd for complete specification.",
	}
}

// formatValidationErrors converts ValidationResult to a user-friendly error.
func formatValidationErrors(result ValidationResult, filePath string) error {
	if result.Valid {
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("invalid PGMI metadata in %s:\n", filePath))

	for i, err := range result.Errors {
		msg.WriteString(fmt.Sprintf("  %d. %s\n", i+1, err))
	}

	msg.WriteString("\nSee metadata format documentation:\n")
	msg.WriteString("  Schema: internal/metadata/schema.xsd\n")
	msg.WriteString("  Generate template: pgmi metadata scaffold <path> --dry-run\n")

	return fmt.Errorf("%s", msg.String())
}
