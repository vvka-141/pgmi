package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestCompleteSSLModes(t *testing.T) {
	cmd := &cobra.Command{}

	t.Run("returns all modes for empty input", func(t *testing.T) {
		completions, directive := completeSSLModes(cmd, nil, "")
		if len(completions) != len(sslModes) {
			t.Errorf("expected %d completions, got %d", len(sslModes), len(completions))
		}
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("expected ShellCompDirectiveNoFileComp, got %v", directive)
		}
	})

	t.Run("filters by prefix", func(t *testing.T) {
		completions, _ := completeSSLModes(cmd, nil, "ver")
		if len(completions) != 2 {
			t.Errorf("expected 2 completions (verify-ca, verify-full), got %d", len(completions))
		}
		for _, c := range completions {
			if c != "verify-ca" && c != "verify-full" {
				t.Errorf("unexpected completion: %s", c)
			}
		}
	})

	t.Run("returns empty for non-matching prefix", func(t *testing.T) {
		completions, _ := completeSSLModes(cmd, nil, "xyz")
		if len(completions) != 0 {
			t.Errorf("expected 0 completions, got %d", len(completions))
		}
	})
}

func TestCompleteDirectories(t *testing.T) {
	cmd := &cobra.Command{}

	t.Run("returns FilterDirs directive for first arg", func(t *testing.T) {
		_, directive := completeDirectories(cmd, nil, "")
		if directive != cobra.ShellCompDirectiveFilterDirs {
			t.Errorf("expected ShellCompDirectiveFilterDirs, got %v", directive)
		}
	})

	t.Run("returns NoFileComp when args already provided", func(t *testing.T) {
		_, directive := completeDirectories(cmd, []string{"./existing"}, "")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("expected ShellCompDirectiveNoFileComp, got %v", directive)
		}
	})
}

func TestCompleteTemplateNames(t *testing.T) {
	cmd := &cobra.Command{}

	t.Run("returns template names", func(t *testing.T) {
		completions, directive := completeTemplateNames(cmd, nil, "")
		if len(completions) == 0 {
			t.Error("expected at least one template completion")
		}
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("expected ShellCompDirectiveNoFileComp, got %v", directive)
		}
		// Check that basic and advanced are present
		foundBasic := false
		foundAdvanced := false
		for _, c := range completions {
			if c == "basic" {
				foundBasic = true
			}
			if c == "advanced" {
				foundAdvanced = true
			}
		}
		if !foundBasic {
			t.Error("expected 'basic' template in completions")
		}
		if !foundAdvanced {
			t.Error("expected 'advanced' template in completions")
		}
	})

	t.Run("filters by prefix", func(t *testing.T) {
		completions, _ := completeTemplateNames(cmd, nil, "bas")
		if len(completions) != 1 || completions[0] != "basic" {
			t.Errorf("expected ['basic'], got %v", completions)
		}
	})

	t.Run("returns NoFileComp when args already provided", func(t *testing.T) {
		_, directive := completeTemplateNames(cmd, []string{"basic"}, "")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("expected ShellCompDirectiveNoFileComp, got %v", directive)
		}
	})
}

func TestCompleteSkillNames(t *testing.T) {
	cmd := &cobra.Command{}

	t.Run("returns skill names", func(t *testing.T) {
		completions, directive := completeSkillNames(cmd, nil, "")
		if len(completions) == 0 {
			t.Error("expected at least one skill completion")
		}
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("expected ShellCompDirectiveNoFileComp, got %v", directive)
		}
	})

	t.Run("returns NoFileComp when args already provided", func(t *testing.T) {
		_, directive := completeSkillNames(cmd, []string{"pgmi-sql"}, "")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("expected ShellCompDirectiveNoFileComp, got %v", directive)
		}
	})
}
