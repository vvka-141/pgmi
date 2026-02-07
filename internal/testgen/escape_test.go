package testgen

import (
	"testing"
)

func TestEscapeSQLString(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"with'quote", "with''quote"},
		{"two''quotes", "two''''quotes"},
		{"path/to/file.sql", "path/to/file.sql"},
		{"it's", "it''s"},
		{"", ""},
	}

	for _, tc := range tests {
		result := EscapeSQLString(tc.input)
		if result != tc.expected {
			t.Errorf("EscapeSQLString(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

func TestEscapeSQLIdentifier(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"with space", `"with space"`},
		{"UPPER", `"UPPER"`},
		{"with-dash", `"with-dash"`},
		{"with\"quote", `"with""quote"`},
		{"__pgmi_0__", "__pgmi_0__"},
		{"_valid_123", "_valid_123"},
	}

	for _, tc := range tests {
		result := EscapeSQLIdentifier(tc.input)
		if result != tc.expected {
			t.Errorf("EscapeSQLIdentifier(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

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

func TestEscapeQualifiedName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"pg_temp.my_func", "pg_temp.my_func"},
		{"UPPER.Func", `"UPPER"."Func"`},
		{"schema.with space", `schema."with space"`},
	}
	for _, tc := range tests {
		result := EscapeQualifiedName(tc.input)
		if result != tc.expected {
			t.Errorf("EscapeQualifiedName(%q) = %q, expected %q", tc.input, result, tc.expected)
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
