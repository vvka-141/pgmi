package testgen

import (
	"fmt"
	"regexp"
	"strings"
)

// EscapeSQLString escapes a string for use in a SQL string literal.
// Single quotes are doubled.
func EscapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// simpleIdentifierPattern matches PostgreSQL identifiers that don't need quoting.
// Must start with letter or underscore, contain only letters, digits, and underscores.
var simpleIdentifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// validIdentifierPattern matches valid PostgreSQL identifiers (case-insensitive).
var validIdentifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// EscapeSQLIdentifier escapes an identifier for PostgreSQL.
// If the identifier is simple lowercase, returns as-is.
// Otherwise, wraps in double quotes and escapes embedded quotes.
func EscapeSQLIdentifier(s string) string {
	if simpleIdentifierPattern.MatchString(s) {
		return s
	}
	// Wrap in double quotes and escape embedded double quotes
	escaped := strings.ReplaceAll(s, `"`, `""`)
	return `"` + escaped + `"`
}

// ValidateCallbackName validates that a callback is a valid PostgreSQL qualified function name.
// Accepts: "function_name" or "schema.function_name"
// Returns nil for empty string (no callback).
func ValidateCallbackName(callback string) error {
	if callback == "" {
		return nil
	}

	parts := strings.Split(callback, ".")
	if len(parts) > 2 {
		return fmt.Errorf("invalid callback %q: expected [schema.]function format", callback)
	}

	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("invalid callback %q: empty identifier", callback)
		}
		if len(part) > 63 {
			return fmt.Errorf("invalid callback %q: identifier %q exceeds 63 character limit", callback, part)
		}
		if !validIdentifierPattern.MatchString(part) {
			return fmt.Errorf("invalid callback %q: %q is not a valid identifier", callback, part)
		}
	}

	return nil
}

// EscapeQualifiedName escapes a schema-qualified name for safe SQL interpolation.
// Input: "schema.function" or "function"
// Output: properly escaped version with each part quoted if needed.
func EscapeQualifiedName(name string) string {
	parts := strings.Split(name, ".")
	for i, part := range parts {
		parts[i] = EscapeSQLIdentifier(part)
	}
	return strings.Join(parts, ".")
}
