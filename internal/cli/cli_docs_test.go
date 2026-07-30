package cli

import (
	"os"
	"strings"
	"testing"
)

func TestCLIReferenceDocumentsTopLevelCommands(t *testing.T) {
	body, err := os.ReadFile("../../docs/CLI.md")
	if err != nil {
		t.Fatalf("read CLI reference: %v", err)
	}

	reference := string(body)
	for _, cmd := range rootCmd.Commands() {
		heading := "## pgmi " + cmd.Name()
		if cmd.Name() == "completion" {
			heading = "## Shell Completion"
		}
		if !strings.Contains(reference, heading) {
			t.Errorf("docs/CLI.md is missing %q for the %q command", heading, cmd.Name())
		}
	}
}
