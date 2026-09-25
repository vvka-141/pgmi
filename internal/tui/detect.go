package tui

import (
	"os"

	"golang.org/x/term"
)

// Mode represents the interaction mode for pgmi.
type Mode int

const (
	// ModeNonInteractive is used for CI/CD pipelines, scripts, and piped input.
	ModeNonInteractive Mode = iota
	// ModeInteractive is used when a human is at the terminal.
	ModeInteractive
)

// DetectMode determines whether pgmi should run in interactive or non-interactive mode.
//
// Returns ModeNonInteractive if:
//   - stdin is not a terminal (piped input, CI/CD)
//   - PGMI_NON_INTERACTIVE=1 is set
//   - CI=true is set (common CI/CD convention)
//
// NO_COLOR is explicitly NOT a signal for non-interactivity — per
// https://no-color.org it disables colors only. Callers that render with
// colors should query ColorsDisabled() instead.
//
// Returns ModeInteractive otherwise.
func DetectMode() Mode {
	if envNonInteractive() || !isTerminal(os.Stdin) {
		return ModeNonInteractive
	}
	// The TUI wizards render to stdout.
	if !isTerminal(os.Stdout) {
		return ModeNonInteractive
	}
	return ModeInteractive
}

// CanPrompt reports whether a human can answer a prompt written to stderr:
// stdin and stderr are terminals. stdout may be piped, so
// `pgmi deploy --overwrite --force | tee log` still gets its Ctrl-C window and
// `--overwrite --json | jq` can still ask for confirmation.
func CanPrompt() bool {
	return !envNonInteractive() && isTerminal(os.Stdin) && isTerminal(os.Stderr)
}

// isTerminal is a seam: under `go test` no descriptor is a terminal, so
// without it every test could only ever observe the non-interactive branch.
var isTerminal = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func envNonInteractive() bool {
	return os.Getenv("PGMI_NON_INTERACTIVE") == "1" || os.Getenv("CI") != ""
}

// IsInteractive is a convenience function that returns true if running in interactive mode.
func IsInteractive() bool {
	return DetectMode() == ModeInteractive
}

// ColorsDisabled returns true when the user requested uncoloured output via
// the NO_COLOR environment variable (https://no-color.org) — an accessibility
// and scripting hint distinct from non-interactivity.
func ColorsDisabled() bool {
	return os.Getenv("NO_COLOR") != ""
}
