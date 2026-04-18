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
	// Check environment overrides first
	if os.Getenv("PGMI_NON_INTERACTIVE") == "1" {
		return ModeNonInteractive
	}
	if os.Getenv("CI") != "" {
		return ModeNonInteractive
	}

	// Check if stdin is a terminal
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return ModeNonInteractive
	}

	// Check if stdout is a terminal (important for TUI rendering)
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return ModeNonInteractive
	}

	return ModeInteractive
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
