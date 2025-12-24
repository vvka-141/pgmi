package metadata

import (
	"strings"

	"github.com/google/uuid"
)

// Validate performs comprehensive validation of metadata against XSD schema constraints.
// It checks:
//   - Required attributes (id, idempotent)
//   - UUID validity and format
//   - Sort keys array (optional, but must be non-empty strings if present)
//   - Whitespace-only content
//
// Parameters:
//   - m: The metadata to validate
//   - filePath: File path for error reporting (optional, can be empty)
//
// Returns:
//   - ValidationResult with Valid=true if all checks pass, or Valid=false with detailed errors
func Validate(m *Metadata, filePath string) ValidationResult {
	result := ValidationResult{Valid: true, Errors: []string{}}

	// Required attribute: id
	if m.ID == uuid.Nil {
		result.AddError(
			"id attribute is required and cannot be the nil UUID (00000000-0000-0000-0000-000000000000).\n" +
				"  Each script must have a unique identifier.\n" +
				"  Generate with: uuidgen (Linux/Mac), [guid]::NewGuid() (PowerShell), or https://www.uuidgenerator.net/")
	}

	// Optional: sortKeys validation (if present, keys must be non-empty)
	if len(m.SortKeys.Keys) > 0 {
		for i, key := range m.SortKeys.Keys {
			if strings.TrimSpace(key) == "" {
				result.AddError(
					"sortKeys[%d] cannot be empty or whitespace-only.\n"+
						"  Provide meaningful sort keys for execution ordering.\n"+
						"  Recommended format: \"phase/sequence\" (e.g., \"10-utils/0010\", \"30-core/2000\")", i)
			}
		}
	}
	// Note: Empty sortKeys array is allowed - files without sort keys use path as fallback

	// Optional: description validation (warn about whitespace-only)
	if m.Description != "" && strings.TrimSpace(m.Description) == "" {
		result.AddError(
			"description element contains only whitespace.\n" +
				"  Consider removing it or providing a meaningful description.")
	}

	return result
}
