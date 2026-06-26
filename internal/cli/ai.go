package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/vvka-141/pgmi/internal/ui"
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
  pgmi ai client [lang]         API client guidance (typescript, python, go, csharp, rust)

Pull commands (overview, skills, skill, client) print to stdout for piping.
setup and check write files and report status on stderr.`,
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

var aiContractCmd = &cobra.Command{
	Use:   "contract",
	Short: "Print the machine-readable session-API contract",
	Long: `Print the pgmi session-API contract as JSON. Agents should query this
before writing SQL against pgmi views/functions to avoid hallucinating
identifiers.

Output includes: view names and columns, test function signatures,
step types, exit codes, and preprocessor macro forms.`,
	Args: cobra.NoArgs,
	RunE: runAIContract,
}

var aiClientCmd = &cobra.Command{
	Use:   "client [lang]",
	Short: "Print API client guidance for AI coding agents",
	Long: `Print guidance for generating a typed API client from the deployment's
live OpenAPI spec. Without a language argument, prints the language-agnostic
doctrine (decision tree, invariants, anti-copy directive). With a language,
adds a transport-core skeleton and recommended generator.

  pgmi ai client              Doctrine only
  pgmi ai client typescript   TypeScript skeleton + openapi-typescript
  pgmi ai client python       Python skeleton + openapi-python-client
  pgmi ai client go           Go skeleton + oapi-codegen
  pgmi ai client csharp       C# skeleton + NSwag
  pgmi ai client rust         Rust skeleton + openapi-generator

This is the APPLICATION API (deployed handlers). For the SESSION API
(temp views/functions for deploy.sql), use pgmi ai contract.`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeClientLangs,
	RunE:              runAIClient,
}

func init() {
	rootCmd.AddCommand(aiCmd)
	aiCmd.AddCommand(aiSkillsCmd)
	aiCmd.AddCommand(aiSkillCmd)
	aiCmd.AddCommand(aiContractCmd)
	aiCmd.AddCommand(aiClientCmd)
}

func runAIOverview(cmd *cobra.Command, args []string) error {
	content, err := ai.GetOverview()
	if err != nil {
		return fmt.Errorf("failed to get AI overview: %w", err)
	}

	ui.Page(content)
	return nil
}

func runAISkills(cmd *cobra.Command, args []string) error {
	skills, err := ai.ListSkills()
	if err != nil {
		return fmt.Errorf("failed to list skills: %w", err)
	}

	w, done := ui.PageWriter()
	defer done()

	fmt.Fprintln(w, "# Available pgmi Skills")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Use `pgmi ai skill <name>` to get full skill content.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Skill | Description |")
	fmt.Fprintln(w, "|-------|-------------|")

	for _, s := range skills {
		desc := s.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(w, "| `%s` | %s |\n", s.Name, desc)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Total: %d skills\n", len(skills))

	return nil
}

func runAISkill(cmd *cobra.Command, args []string) error {
	name := args[0]

	content, err := ai.GetSkill(name)
	if err != nil {
		return err
	}

	ui.Page(content)
	return nil
}

func runAIContract(cmd *cobra.Command, args []string) error {
	out, err := ai.GetContractJSON()
	if err != nil {
		return fmt.Errorf("failed to generate contract: %w", err)
	}
	ui.Page(out + "\n")
	return nil
}

func runAIClient(cmd *cobra.Command, args []string) error {
	doctrine, err := ai.GetClientDoctrine()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		ui.Page(doctrine)
		return nil
	}

	lang := args[0]
	idiom, err := ai.GetClientIdiom(lang)
	if err != nil {
		return err
	}

	if idiom == "" {
		ui.Page(doctrine + fmt.Sprintf("\n---\n\nNo built-in idiom for %q. Any OpenAPI generator works — point it at `/openapi.json`.\n", lang))
		return nil
	}

	ui.Page(idiom)
	return nil
}

func completeClientLangs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var matches []string
	for _, lang := range ai.SupportedClientLangs {
		if strings.HasPrefix(lang, toComplete) {
			matches = append(matches, lang)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}
