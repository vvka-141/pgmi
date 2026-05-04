package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/vvka-141/pgmi/internal/scaffold"
)

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Print a shell completion script to stdout",
	Long: `Print a shell completion script to stdout. Source it in your shell rc.

  pgmi completion bash       > /etc/bash_completion.d/pgmi
  pgmi completion zsh        > "${fpath[1]}/_pgmi"
  pgmi completion fish       > ~/.config/fish/completions/pgmi.fish
  pgmi completion powershell | Out-String | Invoke-Expression`,
	Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return rootCmd.GenBashCompletion(os.Stdout)
		case "zsh":
			return rootCmd.GenZshCompletion(os.Stdout)
		case "fish":
			return rootCmd.GenFishCompletion(os.Stdout, true)
		case "powershell":
			return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
		default:
			return fmt.Errorf("unsupported shell %q (valid: bash, zsh, fish, powershell)", args[0])
		}
	},
}

func init() {
	rootCmd.AddCommand(completionCmd)
}

// sslModes contains valid PostgreSQL SSL modes for shell completion.
var sslModes = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}

// completeTemplateNames provides shell completion for template names.
func completeTemplateNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	templates, err := scaffold.ListTemplates()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	var matches []string
	for _, t := range templates {
		if strings.HasPrefix(t, toComplete) {
			matches = append(matches, t)
		}
	}

	return matches, cobra.ShellCompDirectiveNoFileComp
}

// completeSkillNames provides shell completion for skill names.
func completeSkillNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names, err := ai.GetSkillNames()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	var matches []string
	for _, name := range names {
		if strings.HasPrefix(name, toComplete) {
			matches = append(matches, name)
		}
	}

	return matches, cobra.ShellCompDirectiveNoFileComp
}

// completeSSLModes provides shell completion for SSL mode flag values.
func completeSSLModes(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var matches []string
	for _, mode := range sslModes {
		if strings.HasPrefix(mode, toComplete) {
			matches = append(matches, mode)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}

// completeDirectories provides shell completion for directory paths.
func completeDirectories(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	// Let the shell handle directory completion
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// completeAITemplateNames provides shell completion for AI template documentation names.
func completeAITemplateNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	templates, err := ai.ListTemplateDocs()
	if err != nil || len(templates) == 0 {
		// Fall back to scaffold templates if no AI docs available
		templates, err = scaffold.ListTemplates()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
	}

	var matches []string
	for _, t := range templates {
		if strings.HasPrefix(t, toComplete) {
			matches = append(matches, t)
		}
	}

	return matches, cobra.ShellCompDirectiveNoFileComp
}

