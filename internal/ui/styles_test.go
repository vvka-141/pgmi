package ui

import (
	"testing"

	"github.com/charmbracelet/colorprofile"
)

func TestSuccessIcon_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if got := SuccessIcon(); got != "ok" {
		t.Errorf("SuccessIcon() with NO_COLOR = %q, want %q", got, "ok")
	}
}

func TestFailIcon_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if got := FailIcon(); got != "FAILED" {
		t.Errorf("FailIcon() with NO_COLOR = %q, want %q", got, "FAILED")
	}
}

func TestBold_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if got := Bold("hello"); got != "hello" {
		t.Errorf("Bold() with NO_COLOR = %q, want %q", got, "hello")
	}
}

func TestDim_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if got := Dim("hello"); got != "hello" {
		t.Errorf("Dim() with NO_COLOR = %q, want %q", got, "hello")
	}
}

func TestIcons_FollowTheTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	orig := stderrProfile
	t.Cleanup(func() { stderrProfile = orig })

	stderrProfile = func() colorprofile.Profile { return colorprofile.NoTTY }
	if SuccessIcon() != "ok" || FailIcon() != "FAILED" {
		t.Errorf("a non-terminal stderr gets words: %q %q", SuccessIcon(), FailIcon())
	}
	stderrProfile = func() colorprofile.Profile { return colorprofile.ANSI }
	if SuccessIcon() == "ok" || FailIcon() == "FAILED" {
		t.Errorf("a terminal gets the glyphs: %q %q", SuccessIcon(), FailIcon())
	}
}
