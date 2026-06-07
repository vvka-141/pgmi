package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/vvka-141/pgmi/internal/tui"
)

var (
	setupAssistant  string
	setupGlobal     bool
	setupDryRun     bool
	setupForce      bool
	setupClaudeMd   bool
	setupNoClaudeMd bool

	checkAssistant string
	checkGlobal    bool
)

const claudeMdPointer = "This is a pgmi project — see `.claude/skills/pgmi/` or run `pgmi ai`."

const (
	pointerBegin = "<!-- pgmi:begin -->"
	pointerEnd   = "<!-- pgmi:end -->"
)

var aiSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Write pgmi guidance for a coding assistant",
	Long: `Write a pgmi skill into a coding assistant's skill directory.

  pgmi ai setup                       Detect .claude/, write the Claude skill
  pgmi ai setup --assistant claude    Same, explicit
  pgmi ai setup --assistant agents    Write AGENTS.md (Codex, opencode, etc.)
  pgmi ai setup --assistant codex     Same as agents (--global writes ~/.codex/)
  pgmi ai setup --assistant opencode  Same as agents (--global writes ~/.config/opencode/)
  pgmi ai setup --global              Write to the global location instead
  pgmi ai setup --dry-run             Print planned changes, write nothing

The skill is generated from embedded content and stamped with this binary's
version. Default target is project-local .claude/skills/pgmi/ (commit it to
share). Re-running is idempotent; a hand-edited file is not overwritten without
--force. setup also offers a one-line pgmi pointer in CLAUDE.md
(--claude-md / --no-claude-md to decide without a prompt).`,
	Args: cobra.NoArgs,
	RunE: runAISetup,
}

var aiCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Report whether assistant guidance exists and is current",
	Long: `Report whether pgmi guidance is materialized for a coding assistant and
whether it matches this binary's version.

  pgmi ai check            Check project-local .claude/skills/pgmi/
  pgmi ai check --global   Check ~/.claude/skills/pgmi/

Exits non-zero when guidance is missing, stale, or hand-edited.`,
	Args: cobra.NoArgs,
	RunE: runAICheck,
}

func init() {
	aiCmd.AddCommand(aiSetupCmd)
	aiCmd.AddCommand(aiCheckCmd)

	aiSetupCmd.Flags().StringVar(&setupAssistant, "assistant", "", "Target assistant (claude, agents, codex, opencode, codex-skills)")
	aiSetupCmd.Flags().BoolVar(&setupGlobal, "global", false, "Write to ~/.claude/ instead of the project")
	aiSetupCmd.Flags().BoolVar(&setupDryRun, "dry-run", false, "Print planned file changes, write nothing")
	aiSetupCmd.Flags().BoolVar(&setupForce, "force", false, "Overwrite a hand-edited skill file")
	aiSetupCmd.Flags().BoolVar(&setupClaudeMd, "claude-md", false, "Add the managed pgmi pointer to CLAUDE.md")
	aiSetupCmd.Flags().BoolVar(&setupNoClaudeMd, "no-claude-md", false, "Skip the CLAUDE.md pointer")
	aiSetupCmd.MarkFlagsMutuallyExclusive("claude-md", "no-claude-md")
	_ = aiSetupCmd.RegisterFlagCompletionFunc("assistant", completeAssistantNames)

	aiCheckCmd.Flags().StringVar(&checkAssistant, "assistant", "claude", "Target assistant (claude, agents, codex, opencode, codex-skills)")
	aiCheckCmd.Flags().BoolVar(&checkGlobal, "global", false, "Check ~/.claude/ instead of the project")
	_ = aiCheckCmd.RegisterFlagCompletionFunc("assistant", completeAssistantNames)
}

func runAISetup(cmd *cobra.Command, args []string) error {
	assistant, err := resolveAssistant(setupAssistant)
	if err != nil {
		return err
	}

	if !setupGlobal && !isPgmiProject() {
		return fmt.Errorf("not a pgmi project (no deploy.sql or pgmi.yaml found)\nRun from a pgmi project directory, or use --global for user-wide setup")
	}

	root, err := skillsRoot(assistant, setupGlobal)
	if err != nil {
		return err
	}

	stamp := buildStamp()
	files, err := ai.GenerateSetup(assistant, stamp)
	if err != nil {
		return err
	}

	conflicts := 0
	for _, f := range files {
		target := filepath.Join(root, filepath.FromSlash(f.RelPath))
		action, err := classifyFile(target, f.Content)
		if err != nil {
			return err
		}

		switch action {
		case actionUnchanged:
			// Silent: nothing to do is the common idempotent case.
		case actionConflict:
			if !setupForce {
				conflicts++
				fmt.Fprintf(os.Stderr, "edited     %s (hand-edited; re-run with --force to overwrite)\n", displayPath(target))
				continue
			}
			if err := applyWrite(target, f.Content, "overwrite", "overwrote"); err != nil {
				return err
			}
		case actionCreate:
			if err := applyWrite(target, f.Content, "create", "created"); err != nil {
				return err
			}
		case actionUpdate:
			if err := applyWrite(target, f.Content, "update", "updated"); err != nil {
				return err
			}
		}
	}

	if !setupGlobal && assistant == "claude" {
		if err := maybeWriteClaudeMd(); err != nil {
			return err
		}
	}

	if conflicts > 0 {
		return fmt.Errorf("%d file(s) hand-edited and left unchanged; re-run with --force to overwrite", conflicts)
	}
	if setupDryRun {
		fmt.Fprintln(os.Stderr, "Dry run: no files written.")
	}
	return nil
}

// applyWrite writes a file unless --dry-run, reporting the action on stderr.
func applyWrite(path, content, planVerb, doneVerb string) error {
	if setupDryRun {
		fmt.Fprintf(os.Stderr, "%-10s %s\n", "would "+planVerb, displayPath(path))
		return nil
	}
	if err := writeFile(path, content); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%-10s %s\n", doneVerb, displayPath(path))
	return nil
}

func runAICheck(cmd *cobra.Command, args []string) error {
	assistant, err := resolveAssistant(checkAssistant)
	if err != nil {
		return err
	}

	root, err := skillsRoot(assistant, checkGlobal)
	if err != nil {
		return err
	}

	stamp := buildStamp()
	files, err := ai.GenerateSetup(assistant, stamp)
	if err != nil {
		return err
	}

	needsSetup := false
	for _, f := range files {
		target := filepath.Join(root, filepath.FromSlash(f.RelPath))
		existing, err := os.ReadFile(target)
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "missing  %s\n", displayPath(target))
			needsSetup = true
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", displayPath(target), err)
		}

		cur := ai.ParseManaged(string(existing))
		intended := ai.ParseManaged(f.Content)
		switch {
		case cur.Edited():
			fmt.Fprintf(os.Stderr, "edited   %s (hand-edited since generation)\n", displayPath(target))
			needsSetup = true
		case cur.Body != intended.Body:
			fmt.Fprintf(os.Stderr, "stale    %s (generated by pgmi %s, binary is %s)\n", displayPath(target), orUnknown(cur.Stamp.Version), stamp.Version)
			needsSetup = true
		default:
			fmt.Fprintf(os.Stderr, "current  %s (content is current)\n", displayPath(target))
		}
	}

	if needsSetup {
		return fmt.Errorf("guidance missing, stale, or hand-edited; run `pgmi ai setup`")
	}
	return nil
}

// resolveAssistant validates an explicit assistant name, or picks the default in
// an interactive terminal. Non-interactive contexts must name the assistant.
func resolveAssistant(name string) (string, error) {
	if name == "" {
		if !tui.IsInteractive() {
			return "", fmt.Errorf("--assistant is required in non-interactive mode (supported: %s)", strings.Join(ai.SupportedAssistants, ", "))
		}
		name = "claude"
	}
	if _, err := ai.AdapterFor(name); err != nil {
		return "", err
	}
	return name, nil
}

func skillsRoot(assistant string, global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory: %w", err)
		}
		switch assistant {
		case "codex":
			return filepath.Join(home, ".codex"), nil
		case "codex-skills":
			return filepath.Join(home, ".codex", "skills"), nil
		case "opencode":
			return filepath.Join(home, ".config", "opencode"), nil
		case "agents":
			return home, nil
		default:
			return filepath.Join(home, ".claude", "skills"), nil
		}
	}
	switch assistant {
	case "agents", "codex", "opencode":
		return ".", nil
	case "codex-skills":
		return filepath.Join(".codex", "skills"), nil
	default:
		return filepath.Join(".claude", "skills"), nil
	}
}

func buildStamp() ai.Stamp {
	v, _, _ := resolveVersionInfo()
	return ai.Stamp{Version: v, Source: ai.ModulePath}
}

type fileAction int

const (
	actionCreate fileAction = iota
	actionUpdate
	actionUnchanged
	actionConflict
)

// classifyFile compares the intended content against what is on disk.
func classifyFile(path, intended string) (fileAction, error) {
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return actionCreate, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", displayPath(path), err)
	}

	cur := ai.ParseManaged(string(existing))
	if cur.Edited() {
		return actionConflict, nil
	}
	if cur.Body == ai.ParseManaged(intended).Body {
		return actionUnchanged, nil
	}
	return actionUpdate, nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create %s: %w", displayPath(filepath.Dir(path)), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s: %w", displayPath(path), err)
	}
	return nil
}

// maybeWriteClaudeMd offers or applies the one-line managed pointer in the
// project CLAUDE.md, per the --claude-md / --no-claude-md flags or a prompt.
func maybeWriteClaudeMd() error {
	if setupNoClaudeMd {
		return nil
	}

	path := "CLAUDE.md"
	if setupDryRun {
		// Dry-run never prompts. Only an explicit --claude-md is a definite plan.
		if setupClaudeMd {
			fmt.Fprintf(os.Stderr, "%-10s %s (pgmi pointer)\n", "update", displayPath(path))
		}
		return nil
	}

	want := setupClaudeMd
	if !want && tui.IsInteractive() {
		want = promptYesNo("Add a one-line pgmi pointer to CLAUDE.md?")
	}
	if !want {
		return nil
	}

	changed, err := upsertClaudeMdPointer(path)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(os.Stderr, "%-10s %s (pgmi pointer)\n", "updated", displayPath(path))
	}
	return nil
}

// upsertClaudeMdPointer ensures the managed pointer block exists in CLAUDE.md.
// Returns whether the file changed.
func upsertClaudeMdPointer(path string) (bool, error) {
	block := pointerBegin + "\n" + claudeMdPointer + "\n" + pointerEnd + "\n"

	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return true, os.WriteFile(path, []byte(block), 0644)
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", displayPath(path), err)
	}

	content := string(existing)
	if start := strings.Index(content, pointerBegin); start >= 0 {
		// Replace the managed block in place. The end tag is searched after the
		// begin tag; a dangling begin with no end absorbs to end of file, so a
		// corrupted block self-heals into a single well-formed block.
		tail := ""
		if rel := strings.Index(content[start:], pointerEnd); rel >= 0 {
			tail = content[start+rel+len(pointerEnd):]
		}
		updated := content[:start] + strings.TrimSuffix(block, "\n") + tail
		if updated == content {
			return false, nil
		}
		return true, os.WriteFile(path, []byte(updated), 0644)
	}

	prefix := content
	if !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	return true, os.WriteFile(path, []byte(prefix+"\n"+block), 0644)
}

func promptYesNo(question string) bool {
	fmt.Fprintf(os.Stderr, "%s [Y/n] ", question)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "" || answer == "y" || answer == "yes"
}

// displayPath shows a path relative to the working directory when possible.
func displayPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(cwd, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func isPgmiProject() bool {
	for _, name := range []string{"deploy.sql", "pgmi.yaml"} {
		if _, err := os.Stat(name); err == nil {
			return true
		}
	}
	return false
}

func completeAssistantNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var matches []string
	for _, name := range ai.SupportedAssistants {
		if strings.HasPrefix(name, toComplete) {
			matches = append(matches, name)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}
