package params

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// ParseEnvFile parses environment file content in .env format.
// It returns a map of key-value pairs.
//
// Format rules:
// - Lines starting with # are comments
// - Empty lines are ignored
// - Format: KEY=VALUE
// - Whitespace around = is trimmed
// - Values can be quoted with single or double quotes
// - Unquoted values are trimmed
//
// This implementation is compatible with godotenv behavior for simple cases
// but does not support advanced features like variable expansion or multiline values.
func ParseEnvFile(content []byte) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Find the first = character
		eqIndex := strings.Index(line, "=")
		if eqIndex == -1 {
			return nil, fmt.Errorf("line %d: invalid format, expected KEY=VALUE", lineNum)
		}

		key := strings.TrimSpace(line[:eqIndex])
		value := strings.TrimSpace(line[eqIndex+1:])

		// Validate key is not empty
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", lineNum)
		}

		// Handle quoted values
		if len(value) >= 2 {
			if (strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) ||
				(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
				value = value[1 : len(value)-1]
			}
		}

		result[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading content: %w", err)
	}

	return result, nil
}
