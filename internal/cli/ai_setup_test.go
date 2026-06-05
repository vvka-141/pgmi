package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/ai"
)

func claudeContent(t *testing.T) string {
	t.Helper()
	files, err := ai.GenerateSetup("claude", ai.Stamp{Version: "9.9.9", Source: ai.ModulePath})
	if err != nil {
		t.Fatalf("GenerateSetup: %v", err)
	}
	return files[0].Content
}

func TestResolveAssistant(t *testing.T) {
	if got, err := resolveAssistant("claude"); err != nil || got != "claude" {
		t.Errorf("resolveAssistant(claude) = %q, %v", got, err)
	}
	if _, err := resolveAssistant("codex"); err == nil {
		t.Error("expected error for unsupported assistant")
	}
	// Empty in a non-interactive test process must require --assistant.
	if _, err := resolveAssistant(""); err == nil {
		t.Error("expected error when assistant unset in non-interactive mode")
	}
}

func TestSkillsRoot(t *testing.T) {
	local, err := skillsRoot(false)
	if err != nil {
		t.Fatal(err)
	}
	if local != filepath.Join(".claude", "skills") {
		t.Errorf("local root = %q", local)
	}

	global, err := skillsRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(global) || !strings.HasSuffix(global, filepath.Join(".claude", "skills")) {
		t.Errorf("global root = %q, want absolute path ending in .claude/skills", global)
	}
}

func TestClassifyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	intended := claudeContent(t)

	if a, _ := classifyFile(path, intended); a != actionCreate {
		t.Errorf("missing file: got %v, want create", a)
	}

	if err := os.WriteFile(path, []byte(intended), 0644); err != nil {
		t.Fatal(err)
	}
	if a, _ := classifyFile(path, intended); a != actionUnchanged {
		t.Errorf("identical file: got %v, want unchanged", a)
	}

	olderStamp, err := ai.GenerateSetup("claude", ai.Stamp{Version: "0.0.1", Source: ai.ModulePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(olderStamp[0].Content), 0644); err != nil {
		t.Fatal(err)
	}
	// Same body, only the stamp version differs → still unchanged (idempotent).
	if a, _ := classifyFile(path, intended); a != actionUnchanged {
		t.Errorf("same body different version: got %v, want unchanged", a)
	}

	if err := os.WriteFile(path, []byte("hand written, no stamp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if a, _ := classifyFile(path, intended); a != actionConflict {
		t.Errorf("edited file: got %v, want conflict", a)
	}
}

func TestClassifyFile_UpdateWhenBodyChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")

	onDisk := ai.RenderManaged("# pgmi\n\nold body\n", ai.Stamp{Version: "0.0.1", Source: ai.ModulePath})
	if err := os.WriteFile(path, []byte(onDisk), 0644); err != nil {
		t.Fatal(err)
	}
	if a, _ := classifyFile(path, claudeContent(t)); a != actionUpdate {
		t.Errorf("changed body: got %v, want update", a)
	}
}

func TestUpsertClaudeMdPointer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	changed, err := upsertClaudeMdPointer(path)
	if err != nil || !changed {
		t.Fatalf("create: changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), claudeMdPointer) {
		t.Error("pointer not written on create")
	}

	// Idempotent: second run makes no change.
	changed, err = upsertClaudeMdPointer(path)
	if err != nil || changed {
		t.Errorf("idempotent re-run: changed=%v err=%v", changed, err)
	}

	// Replace an outdated block in place, preserving surrounding content.
	os.WriteFile(path, []byte("# Title\n\nprose\n\n"+pointerBegin+"\nstale text\n"+pointerEnd+"\n"), 0644)
	changed, err = upsertClaudeMdPointer(path)
	if err != nil || !changed {
		t.Fatalf("replace: changed=%v err=%v", changed, err)
	}
	got, _ = os.ReadFile(path)
	s := string(got)
	if !strings.Contains(s, "# Title") || !strings.Contains(s, "prose") {
		t.Error("surrounding content lost on replace")
	}
	if strings.Contains(s, "stale text") {
		t.Error("stale pointer text not replaced")
	}
	if strings.Count(s, pointerBegin) != 1 {
		t.Errorf("expected exactly one managed block, got %d", strings.Count(s, pointerBegin))
	}
}

func TestRunAISetupAndCheck_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("deploy.sql", []byte("-- deploy\n"), 0644); err != nil {
		t.Fatal(err)
	}

	setupAssistant = "claude"
	setupGlobal = false
	setupDryRun = false
	setupForce = false
	setupClaudeMd = false
	setupNoClaudeMd = true
	t.Cleanup(func() { setupNoClaudeMd = false; setupAssistant = "" })

	if err := runAISetup(aiSetupCmd, nil); err != nil {
		t.Fatalf("setup: %v", err)
	}
	skill := filepath.Join(".claude", "skills", "pgmi", "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("SKILL.md not written: %v", err)
	}

	checkAssistant = "claude"
	checkGlobal = false
	if err := runAICheck(aiCheckCmd, nil); err != nil {
		t.Errorf("check after setup should pass: %v", err)
	}

	// Hand-edit → check fails, setup without force fails, setup with force fixes.
	f, _ := os.ReadFile(skill)
	os.WriteFile(skill, append(f, []byte("\nedit\n")...), 0644)
	if err := runAICheck(aiCheckCmd, nil); err == nil {
		t.Error("check should fail on hand-edited file")
	}
	if err := runAISetup(aiSetupCmd, nil); err == nil {
		t.Error("setup without --force should refuse to overwrite edited file")
	}
	setupForce = true
	if err := runAISetup(aiSetupCmd, nil); err != nil {
		t.Errorf("setup --force should overwrite: %v", err)
	}
	setupForce = false
	if err := runAICheck(aiCheckCmd, nil); err != nil {
		t.Errorf("check after --force re-setup should pass: %v", err)
	}
}

func TestRunAISetup_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	setupAssistant = "claude"
	setupGlobal = false
	setupDryRun = true
	setupForce = false
	setupClaudeMd = false
	setupNoClaudeMd = true
	t.Cleanup(func() { setupDryRun = false; setupNoClaudeMd = false; setupAssistant = "" })

	if err := runAISetup(aiSetupCmd, nil); err != nil {
		t.Fatalf("dry-run setup: %v", err)
	}
	if _, err := os.Stat(".claude"); !os.IsNotExist(err) {
		t.Error("dry-run must not create .claude")
	}
}

func TestRunAISetup_DryRunForceWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile("deploy.sql", []byte("-- deploy\n"), 0644)

	// Materialize, then hand-edit so the file is in conflict.
	setupAssistant, setupGlobal, setupDryRun, setupForce = "claude", false, false, false
	setupClaudeMd, setupNoClaudeMd = false, true
	t.Cleanup(func() {
		setupAssistant, setupDryRun, setupForce, setupNoClaudeMd = "", false, false, false
	})
	if err := runAISetup(aiSetupCmd, nil); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(".claude", "skills", "pgmi", "SKILL.md")
	edited := []byte("hand written, no stamp\n")
	os.WriteFile(skill, edited, 0644)

	// --dry-run --force must still write nothing.
	setupDryRun, setupForce = true, true
	if err := runAISetup(aiSetupCmd, nil); err != nil {
		t.Fatalf("dry-run force: %v", err)
	}
	got, _ := os.ReadFile(skill)
	if string(got) != string(edited) {
		t.Error("--dry-run --force must not overwrite the conflicted file")
	}
}

func TestUpsertClaudeMdPointer_DanglingBegin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	// A begin tag with no end tag (corrupted/half-written block).
	os.WriteFile(path, []byte("# Title\n\n"+pointerBegin+"\nleftover\n"), 0644)

	if _, err := upsertClaudeMdPointer(path); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	if strings.Count(s, pointerBegin) != 1 || strings.Count(s, pointerEnd) != 1 {
		t.Errorf("expected exactly one well-formed block, got:\n%s", s)
	}
	if !strings.Contains(s, claudeMdPointer) || !strings.Contains(s, "# Title") {
		t.Errorf("expected pointer and preserved title, got:\n%s", s)
	}
	if strings.Contains(s, "leftover") {
		t.Errorf("dangling content should be absorbed, got:\n%s", s)
	}
}

func TestRunAICheck_MissingFails(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	checkAssistant = "claude"
	checkGlobal = false
	if err := runAICheck(aiCheckCmd, nil); err == nil {
		t.Error("check should fail when guidance is missing")
	}
}
