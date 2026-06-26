package testgen

import (
	"strings"
	"testing"
)

func TestValidateCallbackName(t *testing.T) {
	validCases := []string{
		"",
		"my_callback",
		"pg_temp.my_callback",
		"myschema.my_func",
		"_private",
		"Func123",
		"UPPER_CASE",
	}
	for _, tc := range validCases {
		if err := ValidateCallbackName(tc); err != nil {
			t.Errorf("ValidateCallbackName(%q) should be valid, got error: %v", tc, err)
		}
	}

	invalidCases := []struct {
		input   string
		wantErr string
	}{
		{"schema.func.extra", "expected [schema.]function format"},
		{".", "empty identifier"},
		{"schema.", "empty identifier"},
		{".func", "empty identifier"},
		{"123start", "not a valid identifier"},
		{"has-dash", "not a valid identifier"},
		{"has space", "not a valid identifier"},
		{"func;DROP TABLE", "not a valid identifier"},
	}
	for _, tc := range invalidCases {
		err := ValidateCallbackName(tc.input)
		if err == nil {
			t.Errorf("ValidateCallbackName(%q) should return error", tc.input)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("ValidateCallbackName(%q) error = %q, want containing %q", tc.input, err.Error(), tc.wantErr)
		}
	}
}
