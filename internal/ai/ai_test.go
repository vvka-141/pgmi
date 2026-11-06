package ai

import (
	"strings"
	"testing"
)

func TestGetOverview(t *testing.T) {
	content, err := GetOverview()
	if err != nil {
		t.Fatalf("GetOverview failed: %v", err)
	}

	if content == "" {
		t.Error("Overview content is empty")
	}

	// Verify key sections exist
	expected := []string{
		"pgmi",
		"PostgreSQL",
		"deploy.sql",
		"pgmi_source",
		"`pgmi deploy` loads the project `.env` into its environment; `pgmi serve` does not",
	}

	for _, s := range expected {
		if !strings.Contains(content, s) {
			t.Errorf("Overview missing expected content: %s", s)
		}
	}
}
