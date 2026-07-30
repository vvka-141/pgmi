package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// `templates describe nosuch` exits 2 and lists the available templates.
// `ai skill nosuch` produced an equally good message — it names the offender
// and lists every embedded skill — and exited 1. Same mistake: a name the CLI
// does not know.
//
// The classification lives at the CLI boundary rather than inside
// ai.GetSkill, because the MCP ai_skill tool shares that lookup and reports
// failures as tool results. TestAISkillErrorStaysCleanForMCP below is the other
// half of that decision.
func TestRunAISkill_UnknownSkillIsUsageError(t *testing.T) {
	err := runAISkill(aiSkillCmd, []string{"definitely-not-a-skill"})
	if err == nil {
		t.Fatal("an unknown skill name must fail")
	}
	if !errors.Is(err, pgmi.ErrUsage) {
		t.Errorf("not an ErrUsage chain, so this exits 1 rather than 2: %v", err)
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitUsageError {
		t.Errorf("exit code %d, want %d", got, pgmi.ExitUsageError)
	}
	// The message must keep naming the offender and what is available.
	for _, want := range []string{"definitely-not-a-skill", "Available skills"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing from %v", want, err)
		}
	}
}

// The shared lookup must not gain a CLI vocabulary. An MCP client receives this
// text in a tool result, where "usage error" refers to nothing it did.
func TestAISkillErrorStaysCleanForMCP(t *testing.T) {
	_, err := ai.GetSkill("definitely-not-a-skill")
	if err == nil {
		t.Fatal("an unknown skill name must fail")
	}
	if strings.Contains(err.Error(), "usage error") {
		t.Errorf("the CLI's classification leaked into the shared lookup: %v", err)
	}
	if errors.Is(err, pgmi.ErrUsage) {
		t.Errorf("ai.GetSkill should not carry a CLI exit-code sentinel: %v", err)
	}
}

// A real skill still resolves, so the change did not turn every lookup into an
// error.
func TestRunAISkill_KnownSkillSucceeds(t *testing.T) {
	skills, err := ai.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) == 0 {
		t.Fatal("no embedded skills; the success path cannot be exercised")
	}
	if err := runAISkill(aiSkillCmd, []string{skills[0].Name}); err != nil {
		t.Errorf("known skill %q failed: %v", skills[0].Name, err)
	}
}
