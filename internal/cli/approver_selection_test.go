package cli

import (
	"fmt"
	"testing"

	"github.com/vvka-141/pgmi/internal/ui"
)

// Which approver runs is the decision that determines whether pgmi asks before
// dropping a database. Each approver's own behavior is covered in internal/ui;
// nothing covered the choice between them.
func TestSelectApprover(t *testing.T) {
	tests := []struct {
		name        string
		force       bool
		interactive bool
		want        string
	}{
		{
			name:        "force on a terminal still counts down",
			force:       true,
			interactive: true,
			want:        "*ui.ForcedApprover",
		},
		{
			name:        "force in CI bypasses confirmation",
			force:       true,
			interactive: false,
			want:        "*ui.ForcedApprover",
		},
		{
			name:        "a terminal without force prompts",
			force:       false,
			interactive: true,
			want:        "*ui.InteractiveApprover",
		},
		{
			name:        "no terminal and no force refuses rather than prompting into a pipe",
			force:       false,
			interactive: false,
			want:        "*ui.NonInteractiveApprover",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fmt.Sprintf("%T", selectApprover(tt.force, tt.interactive, false))
			if got != tt.want {
				t.Errorf("selectApprover(force=%v, interactive=%v) = %s, want %s",
					tt.force, tt.interactive, got, tt.want)
			}
		})
	}
}

// The refusing approver must never approve, whatever it is asked about.
func TestNonInteractiveApproverNeverApproves(t *testing.T) {
	approved, err := ui.NewNonInteractiveApprover().RequestApproval(t.Context(), "mydb")
	if approved {
		t.Error("the non-interactive approver approved a destructive overwrite")
	}
	if err == nil {
		t.Error("expected an error explaining that --force is required")
	}
}
