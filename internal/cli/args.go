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

// showHelp backs command groups: Cobra validates Args only on runnable
// commands, so a group without RunE printed help for a mistyped subcommand and
// exited 0.
func showHelp(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}

// RequireProjectPath validates that exactly one project_path argument is provided.
func RequireProjectPath(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		example := cmd.CommandPath() + " ."
		if cmd.Flags().Lookup("database") != nil {
			example += " -d mydb"
		}
		return fmt.Errorf(`%w: missing required argument: <project_path>

Usage: %s

Example:
  %s`, pgmi.ErrUsage, cmd.UseLine(), example)
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
