// Package ai embeds the pgmi documentation that must match the binary: the
// overview printed by `pgmi ai` and the session-API contract.
package ai

import (
	"embed"
	"fmt"
)

//go:embed all:content
var contentFS embed.FS

// GetOverview returns the main AI overview document
func GetOverview() (string, error) {
	content, err := contentFS.ReadFile("content/overview.md")
	if err != nil {
		return "", fmt.Errorf("failed to read overview: %w", err)
	}
	return string(content), nil
}
