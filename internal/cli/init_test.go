package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestRunInit_BasicTemplate(t *testing.T) {
	targetDir := t.TempDir()
	projectDir := filepath.Join(targetDir, "myapp")

	initTemplate = "basic"
	rootCmd.SetArgs([]string{"init", projectDir})
	err := initCmd.RunE(initCmd, []string{projectDir})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	deploySQL := filepath.Join(projectDir, "deploy.sql")
	if _, err := os.Stat(deploySQL); os.IsNotExist(err) {
		t.Error("Expected deploy.sql to exist")
	}
}

func TestRunInit_AdvancedTemplate(t *testing.T) {
	targetDir := t.TempDir()
	projectDir := filepath.Join(targetDir, "myapp")

	initTemplate = "advanced"
	rootCmd.SetArgs([]string{"init", projectDir})
	err := initCmd.RunE(initCmd, []string{projectDir})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	deploySQL := filepath.Join(projectDir, "deploy.sql")
	if _, err := os.Stat(deploySQL); os.IsNotExist(err) {
		t.Error("Expected deploy.sql to exist")
	}
}

// A kept pgmi.yaml names the target; a printed -d would override it.
func TestRunInit_KeptConfigNamesTheTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kept")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte("connection:\n  database: proddb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	initTemplate = "basic"
	out := captureStderr(t, func() {
		if err := initCmd.RunE(initCmd, []string{dir}); err != nil {
			t.Errorf("init: %v", err)
		}
	})
	if strings.Contains(out, "-d mydb") || !strings.Contains(out, "pgmi deploy "+dir+" --param env=dev") {
		t.Errorf("the Next line must omit -d when pgmi.yaml names the database:\n%s", out)
	}
}

func TestNextSteps_QuotesAPathWithASpace(t *testing.T) {
	if out := nextSteps("./my proj", "mydb", "basic"); !strings.Contains(out, "pgmi deploy './my proj' -d mydb --param env=dev") {
		t.Errorf("a spaced path must be quoted:\n%s", out)
	}
}

// The scaffold ships its own agent guidance, so init names it and commits to
// it; pointing at `pgmi ai setup` would write a second, competing copy.
func TestNextSteps_SurfacesDeployAndAgentGuidance(t *testing.T) {
	out := nextSteps("./demo", "mydb", "basic")

	for _, want := range []string{"pgmi deploy ./demo -d mydb", "AGENTS.md", ".agents/skills/"} {
		if !strings.Contains(out, want) {
			t.Errorf("init next-steps must mention %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ai setup") {
		t.Errorf("init next-steps must not send the user to `pgmi ai setup`, got:\n%s", out)
	}
}

// The advanced template refuses a deploy without env, so the printed command
// must pass it; a printed next step that fails is the first thing a newcomer
// runs (PGMI-408).
func TestNextSteps_AdvancedHintRunsAsPrinted(t *testing.T) {
	adv := nextSteps("./demo", "mydb", "advanced")
	if !strings.Contains(adv, "pgmi deploy ./demo -d mydb --param env=dev") {
		t.Errorf("advanced next steps must pass --param env=dev, got:\n%s", adv)
	}
	for _, want := range []string{"env=test", "env=prod", "--params-file"} {
		if !strings.Contains(adv, want) {
			t.Errorf("advanced next steps must point shared databases at %s, got:\n%s", want, adv)
		}
	}
	if basic := nextSteps("./demo", "mydb", "basic"); !strings.Contains(basic, "pgmi deploy ./demo -d mydb --param env=dev\n") {
		t.Errorf("basic requires env too; its next steps must pass --param env=dev, got:\n%s", basic)
	}
	if minimal := nextSteps("./demo", "mydb", "minimal"); strings.Contains(minimal, "env=") {
		t.Errorf("minimal needs no env, got:\n%s", minimal)
	}
}

func TestManagedCloudCaveat(t *testing.T) {
	if got := managedCloudCaveat("basic"); got != "" {
		t.Errorf("basic template should have no managed-cloud caveat, got %q", got)
	}

	adv := managedCloudCaveat("advanced")
	if adv == "" {
		t.Fatal("advanced template should emit a managed-cloud caveat")
	}
	for _, want := range []string{"PostgreSQL 18", "CREATEROLE", "CREATEDB", "PRODUCTION.md#managed-cloud-postgresql"} {
		if !strings.Contains(adv, want) {
			t.Errorf("advanced caveat missing %q; got: %s", want, adv)
		}
	}
}

func TestRunInit_InvalidTemplate(t *testing.T) {
	targetDir := t.TempDir()
	projectDir := filepath.Join(targetDir, "myapp")

	initTemplate = "nonexistent"
	err := initCmd.RunE(initCmd, []string{projectDir})
	if err == nil {
		t.Fatal("Expected error for invalid template")
	}
	if !strings.Contains(err.Error(), "unknown template") {
		t.Errorf("Expected 'unknown template' error, got: %v", err)
	}
}

func TestRunInit_NonEmptyDirectory(t *testing.T) {
	targetDir := t.TempDir()
	os.WriteFile(filepath.Join(targetDir, "existing.txt"), []byte("data"), 0644)

	initTemplate = "basic"
	err := initCmd.RunE(initCmd, []string{targetDir})
	if err == nil {
		t.Fatal("Expected error for non-empty directory")
	}
}

func TestRunInit_CurrentDirectory(t *testing.T) {
	targetDir := t.TempDir()
	emptySubdir := filepath.Join(targetDir, "empty")
	os.MkdirAll(emptySubdir, 0755)

	initTemplate = "basic"
	err := initCmd.RunE(initCmd, []string{emptySubdir})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	deploySQL := filepath.Join(emptySubdir, "deploy.sql")
	if _, err := os.Stat(deploySQL); os.IsNotExist(err) {
		t.Error("Expected deploy.sql to exist")
	}
}

func stubInitWizard(t *testing.T, result wizards.InitResult) {
	t.Helper()
	origInteractive, origWizard := isInteractive, runInitWizard
	t.Cleanup(func() { isInteractive, runInitWizard = origInteractive, origWizard })
	isInteractive = func() bool { return true }
	runInitWizard = func(string) (wizards.InitResult, error) { return result, nil }
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("cancelled init wrote %d entries into %s", len(entries), dir)
	}
}

func TestRunInit_InterruptedWizardWritesNothingExit130(t *testing.T) {
	dir := t.TempDir()
	stubInitWizard(t, wizards.InitResult{Cancelled: true, Interrupted: true, TargetDir: dir, Template: "basic"})

	err := runInit(initCmd, []string{dir})
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitInterrupted {
		t.Errorf("exit code %d, want %d: %v", got, pgmi.ExitInterrupted, err)
	}
	if err != nil && err.Error() != "init interrupted" {
		t.Errorf("message %q, want %q", err.Error(), "init interrupted")
	}
	assertEmptyDir(t, dir)
}

func TestRunInit_CancelledWizardWritesNothingExit12(t *testing.T) {
	dir := t.TempDir()
	stubInitWizard(t, wizards.InitResult{Cancelled: true, TargetDir: dir, Template: "basic"})

	err := runInit(initCmd, []string{dir})
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitApprovalDenied {
		t.Errorf("exit code %d, want %d: %v", got, pgmi.ExitApprovalDenied, err)
	}
	assertEmptyDir(t, dir)
}

func TestQuickstartTreeMatchesInit(t *testing.T) {
	doc, err := os.ReadFile("../../docs/QUICKSTART.md")
	if err != nil {
		t.Fatal(err)
	}
	_, step3, _ := strings.Cut(string(doc), "## Step 3: Create a project")
	_, block, _ := strings.Cut(step3, "```\nmyapp/\n")
	block, _, _ = strings.Cut(block, "```")
	if block == "" {
		t.Fatal("no myapp/ tree in QUICKSTART Step 3")
	}

	dir := filepath.Join(t.TempDir(), "myapp")
	if err := scaffold.NewScaffolder(false).CreateProject("myapp", "basic", dir); err != nil {
		t.Fatal(err)
	}

	var stack []string
	named := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		i := strings.Index(line, "── ")
		if i < 0 {
			continue
		}
		depth := len([]rune(line[:i])) / 4
		name := strings.TrimSuffix(strings.Fields(line[i+len("── "):])[0], "/")
		stack = append(stack[:depth], name)
		p := strings.Join(stack, "/")
		if depth == 0 {
			named[name] = true
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("QUICKSTART names %s, which pgmi init does not write", p)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !named[e.Name()] {
			t.Errorf("pgmi init writes %s, which the QUICKSTART tree leaves out", e.Name())
		}
	}
}

func TestInit_DefaultsToTheMinimalTemplate(t *testing.T) {
	if got := initCmd.Flags().Lookup("template").DefValue; got != "minimal" {
		t.Errorf("--template defaults to %q, want minimal", got)
	}
	if first := wizards.DefaultTemplates()[0].Name; first != "minimal" {
		t.Errorf("the init wizard preselects %q, want minimal like the flag default", first)
	}
}
