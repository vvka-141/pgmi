package metadata

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Every pointer must be reachable from a user's project, not from pgmi's own
// repository: the skill pgmi init scaffolds, and the published guide.
const reference = "Format reference: .agents/skills/pgmi-metadata/SKILL.md (scaffolded by pgmi init) or https://github.com/vvka-141/pgmi/blob/main/docs/METADATA.md"

// MetadataError represents a structured error with context and helpful hints.
// It includes file path, an optional line number, and actionable suggestions.
type MetadataError struct {
	FilePath string // Path to the file with the error
	Line     int    // Line number (0 if unknown)
	Message  string // Primary error message
	Hint     string // Actionable suggestion for fixing
}

// Error implements the error interface with rich formatting.
func (e *MetadataError) Error() string {
	var location string
	if e.Line > 0 {
		location = fmt.Sprintf("%s (line %d)", e.FilePath, e.Line)
	} else {
		location = e.FilePath
	}

	msg := fmt.Sprintf("metadata error in %s: %s", location, e.Message)

	if e.Hint != "" {
		msg += "\n\nHint: " + e.Hint
	}

	return msg
}

// Unwrap reports every metadata failure as invalid configuration, so it exits
// 10 like the other project errors caught before pgmi connects — a duplicate
// <pgmi-meta id>, an unsupported __dunder__ directory, a path that is not a
// directory. A malformed id or a bad idempotent value used to exit 1, which
// tells a CI script nothing about whose fault it is.
func (e *MetadataError) Unwrap() error { return pgmi.ErrInvalidConfig }

// wrapXMLError converts xml package errors to MetadataError with line numbers.
func wrapXMLError(err error, filePath string) error {
	var syntaxErr *xml.SyntaxError
	if errors.As(err, &syntaxErr) {
		return &MetadataError{
			FilePath: filePath,
			Line:     int(syntaxErr.Line),
			Message:  syntaxErr.Msg,
			Hint: "Check that all XML tags are properly closed and attributes are quoted.\n\n" +
				"Expected format:\n" +
				"  <pgmi-meta id=\"UUID\" idempotent=\"true|false\">\n" +
				"    <description>...</description>\n" +
				"    <sortKeys><key>...</key></sortKeys>\n" +
				"  </pgmi-meta>",
		}
	}

	var numErr *strconv.NumError
	switch {
	case errors.As(err, &numErr) && numErr.Func == "ParseBool":
		return &MetadataError{
			FilePath: filePath,
			Message:  fmt.Sprintf("idempotent attribute must be true or false, got %q", numErr.Num),
			Hint:     reference,
		}
	case strings.Contains(err.Error(), "UUID"):
		return &MetadataError{
			FilePath: filePath,
			Message:  "id attribute is not a UUID: " + err.Error(),
			Hint:     "Generate one with SELECT gen_random_uuid();\n" + reference,
		}
	}
	return &MetadataError{
		FilePath: filePath,
		Message:  err.Error(),
		Hint:     reference,
	}
}

// formatValidationErrors converts ValidationResult to a user-friendly error.
func formatValidationErrors(result ValidationResult, filePath string) error {
	if result.Valid {
		return nil
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "invalid PGMI metadata in %s:\n", filePath)

	for i, err := range result.Errors {
		fmt.Fprintf(&msg, "  %d. %s\n", i+1, err)
	}

	msg.WriteString("\nSee metadata format documentation:\n")
	msg.WriteString("  " + reference + "\n")
	msg.WriteString("  Generate template: pgmi metadata scaffold <path>\n")

	// ErrInvalidConfig so this exits 10 like every other project error caught
	// before connecting, rather than the undifferentiated 1.
	return fmt.Errorf("%s: %w", msg.String(), pgmi.ErrInvalidConfig)
}
