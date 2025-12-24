package metadata

import (
	"encoding/xml"
	"fmt"

	"github.com/google/uuid"
)

// Metadata represents the parsed <pgmi-meta> element from SQL file comments.
// This structure maps directly to the XSD schema defined in schema.xsd.
//
// XML Structure:
//
//	<pgmi-meta id="..." idempotent="...">
//	  <description>...</description>
//	  <sortKeys>
//	    <key>10-utils/0010</key>
//	    <key>90-cleanup/9999</key>
//	  </sortKeys>
//	</pgmi-meta>
//
// Multi-Phase Execution:
//   Files can specify multiple sort keys to execute at different deployment stages.
//   Each key in the array results in a separate execution entry in the plan.
type Metadata struct {
	XMLName     xml.Name `xml:"pgmi-meta"`
	ID          uuid.UUID    `xml:"id,attr"`
	Idempotent  bool         `xml:"idempotent,attr"`
	Description string       `xml:"description"`
	SortKeys    SortKeysElement `xml:"sortKeys"`
}

// SortKeysElement represents the <sortKeys> element containing execution keys.
// Each <key> element defines a position where the script should execute.
//
// XML Structure:
//
//	<sortKeys>
//	  <key>10-utils/0010</key>
//	  <key>30-core/2000</key>
//	</sortKeys>
type SortKeysElement struct {
	Keys []string `xml:"key"`
}

// ValidationResult contains the outcome of metadata validation.
// If Valid is false, Errors contains human-readable error messages.
type ValidationResult struct {
	Valid  bool
	Errors []string
}

// AddError appends an error message to the validation result and marks it as invalid.
func (v *ValidationResult) AddError(format string, args ...interface{}) {
	v.Valid = false
	v.Errors = append(v.Errors, fmt.Sprintf(format, args...))
}

// HasErrors returns true if the validation result contains errors.
func (v *ValidationResult) HasErrors() bool {
	return len(v.Errors) > 0
}

// ErrorString returns all validation errors joined with semicolons.
// Returns empty string if no errors.
func (v *ValidationResult) ErrorString() string {
	if len(v.Errors) == 0 {
		return ""
	}
	result := v.Errors[0]
	for i := 1; i < len(v.Errors); i++ {
		result += "; " + v.Errors[i]
	}
	return result
}
