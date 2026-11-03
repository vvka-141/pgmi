package cli

import (
	"fmt"

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
  pgmi ai contract              Session-API contract as JSON

Both print to stdout for piping. A project's own agent guidance
(AGENTS.md and .agents/skills/) is written by pgmi init.`,
	Args: usageArgs(cobra.NoArgs),
	RunE: runAIOverview,
}

var aiContractCmd = &cobra.Command{
	Use:   "contract",
	Short: "Print the machine-readable session-API contract",
	Long: `Print the pgmi session-API contract as JSON. Agents should query this
before writing SQL against pgmi views/functions to avoid hallucinating
identifiers.

Output includes: the stability promise (before 1.0 the contract may change
in a minor release; every change is listed under Upgrading in the release
notes), view names and columns, test function signatures, step types, exit
codes, and preprocessor macro forms.`,
	Args: usageArgs(cobra.NoArgs),
	RunE: runAIContract,
}

func init() {
	rootCmd.AddCommand(aiCmd)
	aiCmd.AddCommand(aiContractCmd)
}

func runAIOverview(cmd *cobra.Command, args []string) error {
	content, err := ai.GetOverview()
	if err != nil {
		return fmt.Errorf("failed to get AI overview: %w", err)
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
