package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// usageArgs wraps a Cobra positional-args validator so its error carries ErrUsage.
func usageArgs(fn cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := fn(cmd, args); err != nil {
			return fmt.Errorf("%w: %w", pgmi.ErrUsage, err)
		}
		return nil
	}
}

// RequireProjectPath validates that exactly one project_path argument is provided.
func RequireProjectPath(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf(`%w: missing required argument: <project_path>

Usage: %s

Example:
  %s . -d mydb`, pgmi.ErrUsage, cmd.UseLine(), cmd.CommandPath())
	}
	if len(args) > 1 {
		return fmt.Errorf("%w: accepts 1 arg(s), received %d", pgmi.ErrUsage, len(args))
	}
	return nil
}

// RequireTemplateName validates that exactly one template_name argument is provided.
func RequireTemplateName(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf(`%w: missing required argument: <template_name>

Usage: %s

Example:
  %s basic

Use 'pgmi templates list' to see available templates.`, pgmi.ErrUsage, cmd.UseLine(), cmd.CommandPath())
	}
	if len(args) > 1 {
		return fmt.Errorf("%w: accepts 1 arg(s), received %d", pgmi.ErrUsage, len(args))
	}
	return nil
}

// RequireSkillName validates that exactly one skill_name argument is provided.
func RequireSkillName(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf(`%w: missing required argument: <name>

Usage: %s

Example:
  %s pgmi-sql

Use 'pgmi ai skills' to see available skills.`, pgmi.ErrUsage, cmd.UseLine(), cmd.CommandPath())
	}
	if len(args) > 1 {
		return fmt.Errorf("%w: accepts 1 arg(s), received %d", pgmi.ErrUsage, len(args))
	}
	return nil
}
