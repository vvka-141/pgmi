package tui

import (
	"os"
	"strings"
	"testing"
)

func TestPromptContinue_ReturnsFalseOnEOF(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin; r.Close() })

	got := promptContinueFromStdin("overwrite?")
	if got {
		t.Error("promptContinueFromStdin() = true on EOF, want false")
	}
}

func TestPromptContinue_AcceptsYes(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"\n", true},
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"no\n", false},
		{"anything\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.WriteString(tt.input); err != nil {
				t.Fatal(err)
			}
			w.Close()

			origStdin := os.Stdin
			os.Stdin = r
			t.Cleanup(func() { os.Stdin = origStdin; r.Close() })

			got := promptContinueFromStdin("test?")
			if got != tt.want {
				t.Errorf("promptContinueFromStdin() with input %q = %v, want %v",
					strings.TrimSpace(tt.input), got, tt.want)
			}
		})
	}
}
