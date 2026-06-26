package ui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/vvka-141/pgmi/internal/tui"
)

var stderrRenderer = lipgloss.NewRenderer(os.Stderr)

func useColor() bool {
	return !tui.ColorsDisabled()
}

func SuccessIcon() string {
	if !useColor() {
		return "ok"
	}
	return stderrRenderer.NewStyle().Foreground(lipgloss.Color("2")).Render("✓")
}

func FailIcon() string {
	if !useColor() {
		return "FAILED"
	}
	return stderrRenderer.NewStyle().Foreground(lipgloss.Color("1")).Render("✗")
}

func Bold(s string) string {
	if !useColor() {
		return s
	}
	return stderrRenderer.NewStyle().Bold(true).Render(s)
}

func Dim(s string) string {
	if !useColor() {
		return s
	}
	return stderrRenderer.NewStyle().Faint(true).Render(s)
}
