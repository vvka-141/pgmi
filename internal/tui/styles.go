package tui

import "github.com/charmbracelet/lipgloss"

// Color palette - keeping it minimal and accessible.
var (
	ColorPrimary   = lipgloss.Color("39")  // Blue
	ColorSecondary = lipgloss.Color("245") // Gray
	ColorSuccess   = lipgloss.Color("34")  // Green
	ColorWarning   = lipgloss.Color("214") // Orange
	ColorError     = lipgloss.Color("196") // Red
	ColorMuted     = lipgloss.Color("240") // Dark gray
)

// Styles for TUI components.
var (
	// Title styles
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			MarginBottom(1)

	SubtitleStyle = lipgloss.NewStyle().
			Foreground(ColorSecondary).
			MarginBottom(1)

	// Box styles for panels
	BoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorSecondary).
			Padding(1, 2)

	FocusedBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimary).
			Padding(1, 2)

	// Input field styles
	InputLabelStyle = lipgloss.NewStyle().
			Foreground(ColorSecondary).
			MarginRight(1)

	InputStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(ColorSecondary).
			Padding(0, 1)

	FocusedInputStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(ColorPrimary).
				Padding(0, 1)

	// Selection styles
	SelectedStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)

	UnselectedStyle = lipgloss.NewStyle().
			Foreground(ColorSecondary)

	DescriptionStyle = lipgloss.NewStyle().
				Foreground(ColorMuted).
				MarginLeft(4)

	// Status styles
	SuccessStyle = lipgloss.NewStyle().
			Foreground(ColorSuccess)

	ErrorStyle = lipgloss.NewStyle().
			Foreground(ColorError)

	WarningStyle = lipgloss.NewStyle().
			Foreground(ColorWarning)

	// Help text style
	HelpStyle = lipgloss.NewStyle().
			Foreground(ColorMuted).
			MarginTop(1)

	// Spinner style
	SpinnerStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary)
)

// Symbols for visual feedback.
const (
	SymbolSelected   = "●"
	SymbolUnselected = "○"
	SymbolCheck      = "✓"
	SymbolCross      = "✗"
	SymbolArrowRight = "→"
	SymbolBullet     = "•"
	SymbolSpinner    = "◐"
)
