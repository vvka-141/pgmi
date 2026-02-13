package cli

import (
	"fmt"
	"os"

	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/spf13/cobra"
)

var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "AI assistant documentation and skills",
	Long: `Machine-readable documentation for AI coding assistants.

This command group provides structured, markdown-formatted documentation
that AI assistants (Claude Code, GitHub Copilot, Gemini CLI, etc.) can
parse and learn from.

When an AI assistant encounters pgmi, it can run these commands to
understand the tool's philosophy, conventions, and best practices.

Example AI workflow:
  1. pgmi ai              # Get overview and index
  2. pgmi ai skills       # List available skills
  3. pgmi ai skill pgmi-sql  # Load SQL conventions`,
	RunE: runAIOverview,
}

var aiSkillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "List all available AI skills",
	Long:  `List all embedded skills with their descriptions.`,
	RunE:  runAISkills,
}

var aiSkillCmd = &cobra.Command{
	Use:               "skill <name>",
	Short:             "Show content of a specific skill",
	Long:              `Output the full content of a skill for AI consumption.`,
	Args:              RequireSkillName,
	ValidArgsFunction: completeSkillNames,
	RunE:              runAISkill,
}

var aiTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "List template documentation",
	Long:  `List available template documentation for AI assistants.`,
	RunE:  runAITemplates,
}

var aiTemplateCmd = &cobra.Command{
	Use:               "template <name>",
	Short:             "Show AI documentation for a template",
	Long:              `Output template-specific documentation and skills.`,
	Args:              RequireTemplateName,
	ValidArgsFunction: completeAITemplateNames,
	RunE:              runAITemplate,
}

func init() {
	rootCmd.AddCommand(aiCmd)
	aiCmd.AddCommand(aiSkillsCmd)
	aiCmd.AddCommand(aiSkillCmd)
	aiCmd.AddCommand(aiTemplatesCmd)
	aiCmd.AddCommand(aiTemplateCmd)
}

func runAIOverview(cmd *cobra.Command, args []string) error {
	content, err := ai.GetOverview()
	if err != nil {
		return fmt.Errorf("failed to get AI overview: %w", err)
	}

	// Output to stdout (not stderr) so AI can capture it
	fmt.Fprint(os.Stdout, content)
	return nil
}

func runAISkills(cmd *cobra.Command, args []string) error {
	skills, err := ai.ListSkills()
	if err != nil {
		return fmt.Errorf("failed to list skills: %w", err)
	}

	fmt.Fprintln(os.Stdout, "# Available pgmi Skills")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Use `pgmi ai skill <name>` to get full skill content.")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "| Skill | Description |")
	fmt.Fprintln(os.Stdout, "|-------|-------------|")

	for _, s := range skills {
		desc := s.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(os.Stdout, "| `%s` | %s |\n", s.Name, desc)
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Total: %d skills\n", len(skills))

	return nil
}

func runAISkill(cmd *cobra.Command, args []string) error {
	name := args[0]

	content, err := ai.GetSkill(name)
	if err != nil {
		return err
	}

	fmt.Fprint(os.Stdout, content)
	return nil
}

func runAITemplates(cmd *cobra.Command, args []string) error {
	templates, err := ai.ListTemplateDocs()
	if err != nil {
		return fmt.Errorf("failed to list templates: %w", err)
	}

	fmt.Fprintln(os.Stdout, "# pgmi Template Documentation")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Use `pgmi ai template <name>` for detailed AI guidance.")
	fmt.Fprintln(os.Stdout)

	if len(templates) == 0 {
		fmt.Fprintln(os.Stdout, "No template documentation embedded yet.")
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "Available templates (use `pgmi templates list`):")
		fmt.Fprintln(os.Stdout, "  - basic: Simple structure for learning")
		fmt.Fprintln(os.Stdout, "  - advanced: Production patterns with API handlers")
		return nil
	}

	fmt.Fprintln(os.Stdout, "| Template | Command |")
	fmt.Fprintln(os.Stdout, "|----------|---------|")
	for _, t := range templates {
		fmt.Fprintf(os.Stdout, "| %s | `pgmi ai template %s` |\n", t, t)
	}

	return nil
}

func runAITemplate(cmd *cobra.Command, args []string) error {
	name := args[0]

	content, err := ai.GetTemplateDoc(name)
	if err != nil {
		// If no embedded doc, try to provide useful fallback
		return fmt.Errorf("template documentation '%s' not found.\n\nUse `pgmi templates describe %s` for basic info, or check the template's README.md after running `pgmi init myproject --template %s`", name, name, name)
	}

	fmt.Fprint(os.Stdout, content)
	return nil
}
