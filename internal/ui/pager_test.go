package ui

import (
	"testing"
)

func TestDefaultPager(t *testing.T) {
	p := defaultPager()
	if p == "" {
		t.Fatal("defaultPager returned empty string")
	}
}

func TestPageWriter_NonInteractive(t *testing.T) {
	t.Setenv("CI", "true")
	w, done := PageWriter()
	defer done()
	if w == nil {
		t.Fatal("PageWriter returned nil writer")
	}
}

func TestResolvePager(t *testing.T) {
	tests := []struct {
		name      string
		pgmiPager string
		pager     string
		want      string
	}{
		{"PGMI_PAGER wins over PAGER", "vim", "less", "vim"},
		{"PGMI_PAGER wins over default", "cat", "", "cat"},
		{"PAGER used when PGMI_PAGER empty", "", "less -R", "less -R"},
		{"default when both empty", "", "", defaultPager()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePager(tt.pgmiPager, tt.pager)
			if got != tt.want {
				t.Errorf("resolvePager(%q, %q) = %q, want %q", tt.pgmiPager, tt.pager, got, tt.want)
			}
		})
	}
}
