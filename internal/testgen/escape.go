package testgen

import (
	"fmt"
	"regexp"
	"strings"
)

var validIdentifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

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
