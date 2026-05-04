package cli

import (
	"fmt"
	"os"

	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/spf13/cobra"
)

var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "Print pgmi documentation for AI assistants (llms.txt-style)",
	Long: `Print machine-readable pgmi documentation. Designed for AI coding assistants
(Claude Code, Copilot, Gemini, Cursor) to ingest as context.

  pgmi ai                       Overview and index (this is the entrypoint)
  pgmi ai skills                List embedded skills
  pgmi ai skill pgmi-sql        Print one skill's full content
  pgmi ai templates             List per-template guides
  pgmi ai template advanced     Print one template's guide

All output goes to stdout for piping or redirection.`,
	RunE: runAIOverview,
}

var aiSkillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "List embedded skills",
	Long:  `Print the name and description of every embedded skill as a markdown table.`,
	RunE:  runAISkills,
}

var aiSkillCmd = &cobra.Command{
	Use:               "skill <name>",
	Short:             "Print one skill's full content",
	Long:              `Print the full markdown content of one embedded skill to stdout.`,
	Args:              RequireSkillName,
	ValidArgsFunction: completeSkillNames,
	RunE:              runAISkill,
}

var aiTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "List per-template AI guides",
	Long:  `Print the names of every embedded template-specific AI guide.`,
	RunE:  runAITemplates,
}

var aiTemplateCmd = &cobra.Command{
	Use:               "template <name>",
	Short:             "Print one template's AI guide",
	Long:              `Print the full markdown of one template's AI guide to stdout.`,
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
		fmt.Fprintln(os.Stdout, "  - basic: Linear migrations, minimal structure")
		fmt.Fprintln(os.Stdout, "  - advanced: Metadata-driven deployment, REST/RPC/MCP handler registry")
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
		return fmt.Errorf("no AI guide embedded for template %q\nrun `pgmi templates describe %s` for the basic structure, or scaffold and read its README", name, name)
	}

	fmt.Fprint(os.Stdout, content)
	return nil
}
