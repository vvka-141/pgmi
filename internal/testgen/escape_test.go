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
