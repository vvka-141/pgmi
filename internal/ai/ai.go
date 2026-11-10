// Package ai embeds the pgmi documentation that must match the binary: the
// overview printed by `pgmi ai` and the session-API contract.
package ai

import (
	"fmt"

	"github.com/vvka-141/pgmi/shared"
)

var contentFS = shared.AI

// GetOverview returns the main AI overview document
func GetOverview() (string, error) {
	content, err := contentFS.ReadFile("ai/overview.md")
	if err != nil {
		return "", fmt.Errorf("failed to read overview: %w", err)
	}
	return string(content), nil
}
