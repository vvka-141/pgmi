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
