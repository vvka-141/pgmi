package metadata

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestExtract_ValidMetadata_AllFields tests extraction with all optional fields present
func TestExtract_ValidMetadata_AllFields(t *testing.T) {
	content := `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>Test script with all fields</description>
  <sortKeys>
    <key>00000000-0000-0000-0000-000000000000/0001</key>
  </sortKeys>
</pgmi-meta>
*/
CREATE TABLE test (id INT);
`

	meta, err := Extract(content, "test.sql")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if meta == nil {
		t.Fatal("Expected metadata, got nil")
	}

	expectedID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	if meta.ID != expectedID {
		t.Errorf("Expected ID %s, got %s", expectedID, meta.ID)
	}

	if !meta.Idempotent {
		t.Error("Expected idempotent=true")
	}

	if len(meta.SortKeys.Keys) != 1 || meta.SortKeys.Keys[0] != "00000000-0000-0000-0000-000000000000/0001" {
		t.Errorf("Expected sortKey '00000000-0000-0000-0000-000000000000/0001', got %v", meta.SortKeys.Keys)
	}

	if meta.Description != "Test script with all fields" {
		t.Errorf("Expected description 'Test script with all fields', got '%s'", meta.Description)
	}
}

// TestExtract_ValidMetadata_MinimalFields tests extraction with only required fields
func TestExtract_ValidMetadata_MinimalFields(t *testing.T) {
	content := `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="false">
  <sortKeys>
    <key>001</key>
  </sortKeys>
</pgmi-meta>
*/
SELECT 1;
`

	meta, err := Extract(content, "minimal.sql")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if meta.Idempotent {
		t.Error("Expected idempotent=false")
	}

	if meta.Description != "" {
		t.Error("Expected empty description")
	}
}

// TestExtract_NoBlockComment tests file with no comments
func TestExtract_NoBlockComment(t *testing.T) {
	content := `SELECT 1 FROM test;`

	_, err := Extract(content, "nocomment.sql")
	if !errors.Is(err, ErrNoMetadata) {
		t.Errorf("Expected ErrNoMetadata, got: %v", err)
	}
}

// TestExtract_NoMetadataInComment tests file with comments but no metadata
func TestExtract_NoMetadataInComment(t *testing.T) {
	content := `/* Just a regular comment */
SELECT 1;
`

	_, err := Extract(content, "nomd.sql")
	if !errors.Is(err, ErrNoMetadata) {
		t.Errorf("Expected ErrNoMetadata, got: %v", err)
	}
}

// TestExtract_MultipleMetadataBlocks tests detection of duplicate metadata
func TestExtract_MultipleMetadataBlocks(t *testing.T) {
	content := `/*
<pgmi-meta id="550e8400-e29b-41d4-a716-446655440000" idempotent="true">
  <sortKeys><key>001</key></sortKeys>
</pgmi-meta>
*/

/*
<pgmi-meta id="7603e3af-b8d9-46a5-8c4c-7f74d39e17f9" idempotent="true">
  <sortKeys><key>002</key></sortKeys>
</pgmi-meta>
*/
`

	_, err := Extract(content, "duplicate.sql")
	if err == nil {
		t.Fatal("Expected error for multiple metadata blocks")
	}

	var metaErr *MetadataError
	if !errors.As(err, &metaErr) {
		t.Errorf("Expected MetadataError, got: %T", err)
	}

	if !strings.Contains(err.Error(), "Multiple metadata blocks found") {
		t.Errorf("Expected 'Multiple metadata blocks' in error, got: %v", err)
	}
}

// TestExtract_OldFormatDetection tests backward compatibility detection
func TestExtract_OldFormatDetection(t *testing.T) {
	content := `/*
<pgmi:meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true"
    xmlns:pgmi="https://pgmi.com/pgmi-metadata/v1">
  <description>Old format</description>
  <sortKeys><key>001</key></sortKeys>
</pgmi:meta>
*/
`

	_, err := Extract(content, "oldformat.sql")
	if err == nil {
		t.Fatal("Expected error for old format")
	}

	var metaErr *MetadataError
	if !errors.As(err, &metaErr) {
		t.Errorf("Expected MetadataError, got: %T", err)
	}

	if !strings.Contains(err.Error(), "old metadata format") {
		t.Errorf("Expected 'old metadata format' in error, got: %v", err)
	}

	if !strings.Contains(metaErr.Hint, "pgmi:meta") {
		t.Error("Expected hint to mention old format")
	}
}

// TestExtract_MetadataTooLarge tests size limit enforcement
func TestExtract_MetadataTooLarge(t *testing.T) {
	// Create metadata block exceeding MaxMetadataSize
	largeDescription := strings.Repeat("x", MaxMetadataSize)
	content := `/*
<pgmi-meta id="550e8400-e29b-41d4-a716-446655440000" idempotent="true">
  <description>` + largeDescription + `</description>
  <sortKeys><key>001</key></sortKeys>
</pgmi-meta>
*/
`

	_, err := Extract(content, "large.sql")
	if err == nil {
		t.Fatal("Expected error for metadata too large")
	}

	var metaErr *MetadataError
	if !errors.As(err, &metaErr) {
		t.Errorf("Expected MetadataError, got: %T", err)
	}

	if !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Errorf("Expected 'exceeds maximum size' in error, got: %v", err)
	}
}

// TestExtract_EmptyMetadataBlock tests whitespace-only metadata
func TestExtract_EmptyMetadataBlock(t *testing.T) {
	content := `/*


*/
SELECT 1;
`

	_, err := Extract(content, "empty.sql")
	if !errors.Is(err, ErrNoMetadata) {
		t.Errorf("Expected ErrNoMetadata for whitespace-only comment, got: %v", err)
	}
}

// TestExtract_InvalidXMLSyntax tests malformed XML handling
func TestExtract_InvalidXMLSyntax(t *testing.T) {
	testCases := []struct {
		name    string
		content string
		errText string
	}{
		{
			name: "unclosed tag",
			content: `/*
<pgmi-meta id="550e8400-e29b-41d4-a716-446655440000" idempotent="true">
  <description>Unclosed
  <sortKeys><key>001</key></sortKeys>
</pgmi-meta>
*/`,
			errText: "metadata error",
		},
		{
			name: "missing closing tag",
			content: `/*
<pgmi-meta id="550e8400-e29b-41d4-a716-446655440000" idempotent="true">
  <description>Test</description>
  <sortKeys><key>001</key></sortKeys>
*/`,
			errText: "metadata error",
		},
		{
			name: "invalid attribute quotes",
			content: `/*
<pgmi-meta id=550e8400-e29b-41d4-a716-446655440000 idempotent="true">
  <sortKeys><key>001</key></sortKeys>
</pgmi-meta>
*/`,
			errText: "metadata error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Extract(tc.content, "invalid.sql")
			if err == nil {
				t.Fatal("Expected error for invalid XML")
			}

			if !strings.Contains(err.Error(), tc.errText) {
				t.Errorf("Expected '%s' in error, got: %v", tc.errText, err)
			}
		})
	}
}

// TestExtract_InvalidUUIDFormat tests UUID parsing errors
func TestExtract_InvalidUUIDFormat(t *testing.T) {
	content := `/*
<pgmi-meta id="not-a-valid-uuid" idempotent="true">
  <sortKeys><key>001</key></sortKeys>
</pgmi-meta>
*/
`

	_, err := Extract(content, "baduuid.sql")
	if err == nil {
		t.Fatal("Expected error for invalid UUID")
	}

	// Should get XML parsing error
	if !strings.Contains(err.Error(), "metadata error") {
		t.Errorf("Expected metadata error, got: %v", err)
	}
}

// TestExtract_MetadataNotInFirstComment tests metadata in later comment
func TestExtract_MetadataNotInFirstComment(t *testing.T) {
	content := `/* First comment - no metadata */

/*
<pgmi-meta id="550e8400-e29b-41d4-a716-446655440000" idempotent="true">
  <description>Metadata in second comment</description>
  <sortKeys><key>001</key></sortKeys>
</pgmi-meta>
*/
SELECT 1;
`

	meta, err := Extract(content, "second.sql")
	if err != nil {
		t.Fatalf("Expected to find metadata in second comment, got error: %v", err)
	}

	if meta.Description != "Metadata in second comment" {
		t.Error("Expected to extract metadata from second comment")
	}
}

// TestExtract_CaseSensitivity tests that element names are case-sensitive
func TestExtract_CaseSensitivity(t *testing.T) {
	content := `/*
<PGMI-META id="550e8400-e29b-41d4-a716-446655440000" idempotent="true">
  <sortKeys><key>001</key></sortKeys>
</PGMI-META>
*/
`

	_, err := Extract(content, "uppercase.sql")
	if !errors.Is(err, ErrNoMetadata) {
		t.Errorf("Expected ErrNoMetadata for uppercase element, got: %v", err)
	}
}

// TestExtract_BooleanParsing tests idempotent attribute parsing
func TestExtract_BooleanParsing(t *testing.T) {
	testCases := []struct {
		value    string
		expected bool
		hasError bool
	}{
		{"true", true, false},
		{"false", false, false},
		{"1", true, false},      // Go XML accepts "1" as true
		{"TRUE", true, false},   // Go XML accepts case insensitive
		{"yes", false, true},    // Should fail - not valid boolean
	}

	for _, tc := range testCases {
		t.Run("idempotent="+tc.value, func(t *testing.T) {
			content := `/*
<pgmi-meta id="550e8400-e29b-41d4-a716-446655440000" idempotent="` + tc.value + `">
  <sortKeys><key>001</key></sortKeys>
</pgmi-meta>
*/
`

			meta, err := Extract(content, "bool.sql")
			if tc.hasError {
				if err == nil {
					t.Fatal("Expected error for invalid boolean value")
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
				if meta.Idempotent != tc.expected {
					t.Errorf("Expected idempotent=%v, got %v", tc.expected, meta.Idempotent)
				}
			}
		})
	}
}

// TestExtractAndValidate_Integration tests the combined extract+validate flow
func TestExtractAndValidate_Integration(t *testing.T) {
	content := `/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>Integration test</description>
  <sortKeys>
    <key>test/001</key>
  </sortKeys>
</pgmi-meta>
*/
SELECT 1;
`

	meta, err := ExtractAndValidate(content, "integration.sql")
	if err != nil {
		t.Fatalf("Expected successful extraction and validation, got: %v", err)
	}

	if meta == nil {
		t.Fatal("Expected metadata, got nil")
	}
}

// TestExtractAndValidate_ValidationFailure tests validation error propagation
func TestExtractAndValidate_ValidationFailure(t *testing.T) {
	// Metadata with nil UUID (validation should fail)
	content := `/*
<pgmi-meta
    id="00000000-0000-0000-0000-000000000000"
    idempotent="true">
  <sortKeys>
    <key></key>
  </sortKeys>
</pgmi-meta>
*/
`

	_, err := ExtractAndValidate(content, "invalid.sql")
	if err == nil {
		t.Fatal("Expected validation error for nil UUID and empty sortKey")
	}

	// Should contain validation errors
	if !strings.Contains(err.Error(), "invalid PGMI metadata") {
		t.Errorf("Expected validation error message, got: %v", err)
	}
}
