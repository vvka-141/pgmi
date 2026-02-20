package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// RequireProjectPath validates that exactly one project_path argument is provided.
// Returns a helpful error message with usage and examples if missing or too many.
func RequireProjectPath(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf(`missing required argument: <project_path>

Usage: %s <project_path>

Example:
  %s ./migrations -d mydb`, cmd.UseLine(), cmd.CommandPath())
	}
	if len(args) > 1 {
		return fmt.Errorf("accepts 1 arg(s), received %d", len(args))
	}
	return nil
}

// RequireTemplateName validates that exactly one template_name argument is provided.
// Returns a helpful error message with usage and examples if missing or too many.
func RequireTemplateName(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf(`missing required argument: <template_name>

Usage: %s <template_name>

Example:
  %s basic

Use 'pgmi templates list' to see available templates.`, cmd.UseLine(), cmd.CommandPath())
	}
	if len(args) > 1 {
		return fmt.Errorf("accepts 1 arg(s), received %d", len(args))
	}
	return nil
}

// RequireSkillName validates that exactly one skill_name argument is provided.
// Returns a helpful error message with usage and examples if missing or too many.
func RequireSkillName(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf(`missing required argument: <name>

Usage: %s <name>

Example:
  %s pgmi-sql

Use 'pgmi ai skills' to see available skills.`, cmd.UseLine(), cmd.CommandPath())
	}
	if len(args) > 1 {
		return fmt.Errorf("accepts 1 arg(s), received %d", len(args))
	}
	return nil
}
