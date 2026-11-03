package preprocessor

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var validIdentifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validateCallbackName validates that a callback is a valid PostgreSQL qualified function name.
// Accepts: "function_name" or "schema.function_name"
// Returns nil for empty string (no callback).
func validateCallbackName(callback string) error {
	if callback == "" {
		return nil
	}

	parts := strings.Split(callback, ".")
	if len(parts) > 2 {
		return fmt.Errorf("invalid callback %q: expected [schema.]function format: %w", callback, pgmi.ErrInvalidConfig)
	}

	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("invalid callback %q: empty identifier: %w", callback, pgmi.ErrInvalidConfig)
		}
		if len(part) > 63 {
			return fmt.Errorf("invalid callback %q: identifier %q exceeds 63 character limit: %w", callback, part, pgmi.ErrInvalidConfig)
		}
		if !validIdentifierPattern.MatchString(part) {
			return fmt.Errorf("invalid callback %q: %q is not a valid identifier: %w", callback, part, pgmi.ErrInvalidConfig)
		}
	}

	return nil
}
