package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/ai"
)

var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "Print pgmi documentation for AI assistants (llms.txt-style)",
	Long: `Print machine-readable pgmi documentation. Designed for AI coding assistants
(Claude Code, Copilot, Gemini, Cursor) to ingest as context.

  pgmi ai                       Overview and index (this is the entrypoint)
  pgmi ai skills                List embedded skills
  pgmi ai skill pgmi-sql        Print one skill's full content
  pgmi ai setup                 Write pgmi guidance into .claude/skills/pgmi/
  pgmi ai check                 Report whether that guidance is current

Pull commands (overview, skills, skill) print to stdout for piping. setup and
check write files and report status on stderr.`,
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

func init() {
	rootCmd.AddCommand(aiCmd)
	aiCmd.AddCommand(aiSkillsCmd)
	aiCmd.AddCommand(aiSkillCmd)
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
