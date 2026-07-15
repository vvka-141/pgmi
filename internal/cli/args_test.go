package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRequireArgValidators(t *testing.T) {
	tests := []struct {
		name       string
		fn         func(*cobra.Command, []string) error
		cmdUse     string
		validArg   string
		missingMsg string
		helpMsg    string
	}{
		{
			name:       "RequireProjectPath",
			fn:         RequireProjectPath,
			cmdUse:     "deploy <project_path>",
			validArg:   ".",
			missingMsg: "missing required argument: <project_path>",
			helpMsg:    "Example:",
		},
		{
			name:       "RequireTemplateName",
			fn:         RequireTemplateName,
			cmdUse:     "describe <template_name>",
			validArg:   "basic",
			missingMsg: "missing required argument: <template_name>",
			helpMsg:    "pgmi templates list",
		},
		{
			name:       "RequireSkillName",
			fn:         RequireSkillName,
			cmdUse:     "skill <name>",
			validArg:   "pgmi-sql",
			missingMsg: "missing required argument: <name>",
			helpMsg:    "pgmi ai skills",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: tt.cmdUse}

			t.Run("missing arg", func(t *testing.T) {
				err := tt.fn(cmd, []string{})
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.missingMsg) {
					t.Errorf("expected %q, got: %s", tt.missingMsg, err.Error())
				}
				if !strings.Contains(err.Error(), tt.helpMsg) {
					t.Errorf("expected %q, got: %s", tt.helpMsg, err.Error())
				}
			})

			t.Run("valid arg", func(t *testing.T) {
				if err := tt.fn(cmd, []string{tt.validArg}); err != nil {
					t.Errorf("expected nil, got: %v", err)
				}
			})

			t.Run("too many args", func(t *testing.T) {
				err := tt.fn(cmd, []string{"a", "b"})
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), "accepts 1 arg") {
					t.Errorf("expected 'accepts 1 arg', got: %s", err.Error())
				}
			})
		})
	}
}
