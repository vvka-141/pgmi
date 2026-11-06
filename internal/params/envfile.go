package params

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// ParseEnvFile parses a params file: KEY=VALUE lines, # comments, an
// optional "export " prefix, values literal or in single or double quotes.
// The project .env is read by godotenv, which strips an inline " # comment",
// expands $NAME and decodes escapes in double quotes; a line it would read
// differently is refused, naming the line but never the value, so a secret
// never arrives changed.
func ParseEnvFile(content []byte) (map[string]string, error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	result := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		eqIndex := strings.Index(line, "=")
		if eqIndex == -1 {
			return nil, fmt.Errorf("line %d: invalid format, expected KEY=VALUE", lineNum)
		}
		key := strings.TrimSpace(line[:eqIndex])
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", lineNum)
		}

		value, ambiguous := literalValue(strings.TrimSpace(line[eqIndex+1:]))
		if ambiguous != "" {
			return nil, fmt.Errorf("line %d: the value of %s %s, so a .env file reads it differently; put it in single quotes", lineNum, key, ambiguous)
		}
		result[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading content: %w", err)
	}
	return result, nil
}

// literalValue unquotes value, or says why a .env file would read it
// differently.
func literalValue(value string) (string, string) {
	if value == "" {
		return "", ""
	}
	if q := value[0]; q == '\'' || q == '"' {
		if len(value) < 2 || value[len(value)-1] != q || strings.IndexByte(value[1:len(value)-1], q) >= 0 {
			return "", "has an unterminated quote or text after the closing one"
		}
		inner := value[1 : len(value)-1]
		if q == '"' && strings.ContainsAny(inner, "\\$") {
			return "", "has a backslash or $ inside double quotes"
		}
		return inner, ""
	}
	if strings.HasPrefix(value, "#") || strings.Contains(value, " #") || strings.Contains(value, "\t#") {
		return "", "has # after a space, where a comment starts"
	}
	if strings.Contains(value, "$") {
		return "", "has $, where a variable expands"
	}
	return value, ""
}
