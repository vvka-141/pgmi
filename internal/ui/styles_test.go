package ui

import (
	"testing"
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
