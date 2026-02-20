package tui

import (
	"testing"
)

func TestDetectMode_PGMI_NON_INTERACTIVE(t *testing.T) {
	t.Setenv("PGMI_NON_INTERACTIVE", "1")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")

	if got := DetectMode(); got != ModeNonInteractive {
		t.Errorf("DetectMode() = %d, want ModeNonInteractive", got)
	}
}

func TestDetectMode_CI(t *testing.T) {
	t.Setenv("PGMI_NON_INTERACTIVE", "")
	t.Setenv("CI", "true")
	t.Setenv("NO_COLOR", "")

	if got := DetectMode(); got != ModeNonInteractive {
		t.Errorf("DetectMode() = %d, want ModeNonInteractive", got)
	}
}

func TestDetectMode_NO_COLOR(t *testing.T) {
	t.Setenv("PGMI_NON_INTERACTIVE", "")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "1")

	if got := DetectMode(); got != ModeNonInteractive {
		t.Errorf("DetectMode() = %d, want ModeNonInteractive", got)
	}
}

func TestDetectMode_NoTerminal(t *testing.T) {
	// In test context, stdin/stdout are not terminals
	t.Setenv("PGMI_NON_INTERACTIVE", "")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")

	if got := DetectMode(); got != ModeNonInteractive {
		t.Errorf("DetectMode() = %d, want ModeNonInteractive (no terminal in test)", got)
	}
}

func TestIsInteractive_ReturnsFalseInTests(t *testing.T) {
	t.Setenv("PGMI_NON_INTERACTIVE", "")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")

	if IsInteractive() {
		t.Error("IsInteractive() = true in test environment, want false")
	}
}

func TestDetectMode_PGMI_NON_INTERACTIVE_TakesPrecedence(t *testing.T) {
	// Even if CI and NO_COLOR are unset, PGMI_NON_INTERACTIVE=1 should trigger non-interactive
	t.Setenv("PGMI_NON_INTERACTIVE", "1")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")

	if got := DetectMode(); got != ModeNonInteractive {
		t.Errorf("DetectMode() = %d, want ModeNonInteractive", got)
	}
}

func TestDetectMode_PGMI_NON_INTERACTIVE_WrongValue(t *testing.T) {
	// Only "1" triggers non-interactive, not "true" or "yes"
	t.Setenv("PGMI_NON_INTERACTIVE", "true")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")

	// Falls through to terminal check (which returns non-interactive in tests)
	if got := DetectMode(); got != ModeNonInteractive {
		t.Errorf("DetectMode() = %d, want ModeNonInteractive (no terminal)", got)
	}
}
