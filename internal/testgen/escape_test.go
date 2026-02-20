package testgen

import (
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
		if !contains(err.Error(), tc.wantErr) {
			t.Errorf("ValidateCallbackName(%q) error = %q, want containing %q", tc.input, err.Error(), tc.wantErr)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
