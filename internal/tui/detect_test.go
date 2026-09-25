package tui

import (
	"os"
	"testing"
)

// terminals fakes which standard descriptors are TTYs and clears the
// environment overrides, so both branches of every check are reachable under
// `go test`, where no descriptor is a terminal.
func terminals(t *testing.T, stdin, stdout, stderr bool) {
	t.Helper()
	t.Setenv("PGMI_NON_INTERACTIVE", "")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")
	orig := isTerminal
	t.Cleanup(func() { isTerminal = orig })
	isTerminal = func(f *os.File) bool {
		switch f {
		case os.Stdin:
			return stdin
		case os.Stdout:
			return stdout
		case os.Stderr:
			return stderr
		}
		return false
	}
}

func TestDetectMode_AllTerminalsIsInteractive(t *testing.T) {
	terminals(t, true, true, true)
	if got := DetectMode(); got != ModeInteractive {
		t.Errorf("DetectMode() = %d, want ModeInteractive", got)
	}
}

func TestDetectMode_EnvOverridesWinOverTerminals(t *testing.T) {
	for _, env := range [][2]string{{"PGMI_NON_INTERACTIVE", "1"}, {"CI", "true"}} {
		t.Run(env[0], func(t *testing.T) {
			terminals(t, true, true, true)
			t.Setenv(env[0], env[1])
			if got := DetectMode(); got != ModeNonInteractive {
				t.Errorf("DetectMode() = %d, want ModeNonInteractive", got)
			}
			if CanPrompt() {
				t.Error("CanPrompt() = true, want false")
			}
		})
	}
}

// Only "1" disables interactivity; "true" is not the documented value.
func TestDetectMode_PGMI_NON_INTERACTIVE_OnlyOne(t *testing.T) {
	terminals(t, true, true, true)
	t.Setenv("PGMI_NON_INTERACTIVE", "true")
	if got := DetectMode(); got != ModeInteractive {
		t.Errorf("DetectMode() = %d, want ModeInteractive", got)
	}
}

// NO_COLOR disables colours only (https://no-color.org), never interactivity.
func TestDetectMode_NO_COLORIsNotNonInteractive(t *testing.T) {
	terminals(t, true, true, true)
	t.Setenv("NO_COLOR", "1")
	if got := DetectMode(); got != ModeInteractive {
		t.Errorf("DetectMode() = %d, want ModeInteractive", got)
	}
}

// The DROP prompt and the --force countdown write to stderr, so piping stdout
// (`| tee log`, `--json | jq`) must not take them away (PGMI-382).
func TestCanPrompt_StdoutPiped(t *testing.T) {
	terminals(t, true, false, true)
	if !CanPrompt() {
		t.Error("CanPrompt() = false with stdin and stderr on a terminal")
	}
	if DetectMode() != ModeNonInteractive {
		t.Error("DetectMode() must stay non-interactive: the TUI wizards render to stdout")
	}
}

func TestCanPrompt_StderrOrStdinRedirected(t *testing.T) {
	for _, c := range []struct {
		name          string
		stdin, stderr bool
	}{{"stderr redirected", true, false}, {"stdin redirected", false, true}} {
		t.Run(c.name, func(t *testing.T) {
			terminals(t, c.stdin, true, c.stderr)
			if CanPrompt() {
				t.Error("CanPrompt() = true, want false")
			}
		})
	}
}
