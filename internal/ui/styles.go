package ui

import (
	"os"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/vvka-141/pgmi/internal/tui"
)

var stderrProfile = sync.OnceValue(func() colorprofile.Profile {
	return colorprofile.Detect(os.Stderr, os.Environ())
})

// useColor is false for NO_COLOR and for a stderr that is no terminal (a CI
// log, a pipe, TERM=dumb): glyphs and escapes only reach a person's screen.
func useColor() bool {
	return !tui.ColorsDisabled() && stderrProfile() != colorprofile.NoTTY
}

func render(style lipgloss.Style, s string) string {
	var b strings.Builder
	w := colorprofile.Writer{Forward: &b, Profile: stderrProfile()}
	_, _ = w.Write([]byte(style.Render(s)))
	return b.String()
}

func SuccessIcon() string {
	if !useColor() {
		return "ok"
	}
	return render(lipgloss.NewStyle().Foreground(lipgloss.Color("2")), "✓")
}

func FailIcon() string {
	if !useColor() {
		return "FAILED"
	}
	return render(lipgloss.NewStyle().Foreground(lipgloss.Color("1")), "✗")
}

func Bold(s string) string {
	if !useColor() {
		return s
	}
	return render(lipgloss.NewStyle().Bold(true), s)
}

func Dim(s string) string {
	if !useColor() {
		return s
	}
	return render(lipgloss.NewStyle().Faint(true), s)
}
