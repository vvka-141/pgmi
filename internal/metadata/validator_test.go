package metadata

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestValidate_AllValid tests validation of fully valid metadata
func TestValidate_AllValid(t *testing.T) {
	meta := &Metadata{
		ID:          uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		Idempotent:  true,
		SortKeys:    SortKeysElement{Keys: []string{"test/001"}},
		Description: "Test script",
	}

	result := Validate(meta, "test.sql")
	if !result.Valid {
		t.Errorf("Expected valid result, got errors: %v", result.Errors)
	}

	if len(result.Errors) != 0 {
		t.Errorf("Expected no errors, got: %v", result.Errors)
	}
}

// TestValidate_MinimalValid tests validation with only required fields
func TestValidate_MinimalValid(t *testing.T) {
	meta := &Metadata{
		ID:         uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		Idempotent: false,
		SortKeys:   SortKeysElement{Keys: []string{"001"}},
	}

	result := Validate(meta, "minimal.sql")
	if !result.Valid {
		t.Errorf("Expected valid result, got errors: %v", result.Errors)
	}
}

// TestValidate_NilUUID tests rejection of nil UUID
func TestValidate_NilUUID(t *testing.T) {
	meta := &Metadata{
		ID:         uuid.Nil,
		Idempotent: true,
		SortKeys:   SortKeysElement{Keys: []string{"001"}},
	}

	result := Validate(meta, "nil.sql")
	if result.Valid {
		t.Error("Expected invalid result for nil UUID")
	}

	if len(result.Errors) == 0 {
		t.Fatal("Expected error for nil UUID")
	}

	errorText := result.ErrorString()
	if !strings.Contains(errorText, "nil UUID") {
		t.Errorf("Expected error about nil UUID, got: %s", errorText)
	}

	if !strings.Contains(errorText, "uuidgen") {
		t.Error("Expected hint about generating UUID")
	}
}

// TestValidate_EmptySortKey tests rejection of empty sortKey
func TestValidate_EmptySortKey(t *testing.T) {
	meta := &Metadata{
		ID:         uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		Idempotent: true,
		SortKeys:   SortKeysElement{Keys: []string{""}},
	}

	result := Validate(meta, "emptysort.sql")
	if result.Valid {
		t.Error("Expected invalid result for empty sortKey")
	}

	errorText := result.ErrorString()
	if !strings.Contains(errorText, "sortKeys") {
		t.Errorf("Expected error about sortKeys, got: %s", errorText)
	}
}

// TestValidate_WhitespaceOnlySortKey tests rejection of whitespace-only sortKey
func TestValidate_WhitespaceOnlySortKey(t *testing.T) {
	meta := &Metadata{
		ID:         uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		Idempotent: true,
		SortKeys:   SortKeysElement{Keys: []string{"   \t\n  "}},
	}

	result := Validate(meta, "spacesort.sql")
	if result.Valid {
		t.Error("Expected invalid result for whitespace-only sortKey")
	}

	errorText := result.ErrorString()
	if !strings.Contains(errorText, "whitespace") && !strings.Contains(errorText, "empty") {
		t.Errorf("Expected error about whitespace or empty, got: %s", errorText)
	}
}

// TestValidate_WhitespaceOnlyDescription tests warning for whitespace-only description
func TestValidate_WhitespaceOnlyDescription(t *testing.T) {
	meta := &Metadata{
		ID:          uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		Idempotent:  true,
		SortKeys:    SortKeysElement{Keys: []string{"001"}},
		Description: "   \n\t   ",
	}

	result := Validate(meta, "spacedesc.sql")
	if result.Valid {
		t.Error("Expected invalid result for whitespace-only description")
	}

	errorText := result.ErrorString()
	if !strings.Contains(errorText, "whitespace") {
		t.Errorf("Expected warning about whitespace description, got: %s", errorText)
	}
}


// TestValidate_MultipleErrors tests that all errors are collected
func TestValidate_MultipleErrors(t *testing.T) {
	meta := &Metadata{
		ID:          uuid.Nil,                            // Error 1: nil ID
		Idempotent:  true,
		SortKeys:    SortKeysElement{Keys: []string{""}}, // Error 2: empty sortKey
		Description: "   ",                               // Error 3: whitespace description
	}

	result := Validate(meta, "multiple.sql")
	if result.Valid {
		t.Error("Expected invalid result with multiple errors")
	}

	if len(result.Errors) < 3 {
		t.Errorf("Expected at least 3 errors, got %d: %v", len(result.Errors), result.Errors)
	}

	// Verify all errors are present
	errorText := result.ErrorString()
	expectedSubstrings := []string{"id attribute", "sortKeys", "whitespace"}
	for _, expected := range expectedSubstrings {
		if !strings.Contains(errorText, expected) {
			t.Errorf("Expected error text to contain '%s', got: %s", expected, errorText)
		}
	}
}

// TestValidationResult_AddError tests error accumulation
func TestValidationResult_AddError(t *testing.T) {
	result := ValidationResult{Valid: true, Errors: []string{}}

	if !result.Valid {
		t.Error("Expected initial state to be valid")
	}

	result.AddError("First error")
	if result.Valid {
		t.Error("Expected valid=false after adding error")
	}

	if len(result.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(result.Errors))
	}

	result.AddError("Second error: %s", "details")
	if len(result.Errors) != 2 {
		t.Errorf("Expected 2 errors, got %d", len(result.Errors))
	}

	if !strings.Contains(result.Errors[1], "details") {
		t.Error("Expected formatted error with details")
	}
}

// TestValidationResult_ErrorString tests error string formatting
func TestValidationResult_ErrorString(t *testing.T) {
	result := ValidationResult{Valid: false, Errors: []string{}}

	// Test empty errors
	if result.ErrorString() != "" {
		t.Errorf("Expected empty string for no errors, got: %s", result.ErrorString())
	}

	// Test single error
	result.AddError("Error 1")
	errorStr := result.ErrorString()
	if errorStr != "Error 1" {
		t.Errorf("Expected 'Error 1', got: %s", errorStr)
	}

	// Test multiple errors with semicolon separator
	result.AddError("Error 2")
	result.AddError("Error 3")
	errorStr = result.ErrorString()
	if !strings.Contains(errorStr, ";") {
		t.Error("Expected semicolon separator in error string")
	}

	if !strings.Contains(errorStr, "Error 1") || !strings.Contains(errorStr, "Error 2") || !strings.Contains(errorStr, "Error 3") {
		t.Errorf("Expected all errors in string, got: %s", errorStr)
	}
}

func TestValidationResult_AddError_MarksInvalid(t *testing.T) {
	result := ValidationResult{Valid: true, Errors: []string{}}

	if !result.Valid {
		t.Error("Expected valid initially")
	}

	result.AddError("Test error")
	if result.Valid {
		t.Error("Expected invalid after adding error")
	}
}
