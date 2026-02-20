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
//   - NO_COLOR is set (accessibility/automation indicator)
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
	if os.Getenv("NO_COLOR") != "" {
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
