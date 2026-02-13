package params

import (
	"fmt"
	"strings"
)

// ParseKeyValuePairs converts a slice of "key=value" strings into a map.
// Uses strings.Cut() (Go 1.18+) for cleaner parsing.
//
// Example:
//
//	params, err := ParseKeyValuePairs([]string{"env=prod", "dbName=myapp"})
//	// Returns: map[string]string{"env": "prod", "dbName": "myapp"}
func ParseKeyValuePairs(pairs []string) (map[string]string, error) {
	result := make(map[string]string, len(pairs))

	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("parameter %q is not in key=value format (example: --param env=production)", pair)
		}

		if key == "" {
			return nil, fmt.Errorf("parameter has empty key: %q", pair)
		}

		result[key] = value
	}

	return result, nil
}
