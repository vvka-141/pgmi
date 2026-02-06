package testgen

import (
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
