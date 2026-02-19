package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Option represents a selectable option in the selector.
type Option struct {
	Label       string
	Description string
	Value       string
}

// Selector is a component for selecting from a list of options.
type Selector struct {
	title       string
	options     []Option
	cursor      int
	selected    int
	width       int
	showHelp    bool
	keyMap      selectorKeyMap
	styles      selectorStyles
	submitted   bool
	cancelled   bool
}

type selectorKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Quit   key.Binding
}

type selectorStyles struct {
	Title       lipgloss.Style
	Selected    lipgloss.Style
	Unselected  lipgloss.Style
	Description lipgloss.Style
	Help        lipgloss.Style
}

func defaultSelectorStyles() selectorStyles {
	return selectorStyles{
		Title:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).MarginBottom(1),
		Selected:    lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true),
		Unselected:  lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		Description: lipgloss.NewStyle().Foreground(lipgloss.Color("240")).MarginLeft(4),
		Help:        lipgloss.NewStyle().Foreground(lipgloss.Color("240")).MarginTop(1),
	}
}

func defaultSelectorKeyMap() selectorKeyMap {
	return selectorKeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Select: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "select"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c", "esc"),
			key.WithHelp("q/esc", "quit"),
		),
	}
}

// NewSelector creates a new selector component.
func NewSelector(title string, options []Option) Selector {
	return Selector{
		title:    title,
		options:  options,
		cursor:   0,
		selected: -1,
		width:    60,
		showHelp: true,
		keyMap:   defaultSelectorKeyMap(),
		styles:   defaultSelectorStyles(),
	}
}

// WithWidth sets the width of the selector.
func (s Selector) WithWidth(width int) Selector {
	s.width = width
	return s
}

// WithShowHelp enables or disables the help text.
func (s Selector) WithShowHelp(show bool) Selector {
	s.showHelp = show
	return s
}

// Init implements tea.Model.
func (s Selector) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (s Selector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, s.keyMap.Up):
			if s.cursor > 0 {
				s.cursor--
			}
		case key.Matches(msg, s.keyMap.Down):
			if s.cursor < len(s.options)-1 {
				s.cursor++
			}
		case key.Matches(msg, s.keyMap.Select):
			s.selected = s.cursor
			s.submitted = true
			return s, tea.Quit
		case key.Matches(msg, s.keyMap.Quit):
			s.cancelled = true
			return s, tea.Quit
		}
	case tea.WindowSizeMsg:
		s.width = msg.Width
	}
	return s, nil
}

// View implements tea.Model.
func (s Selector) View() string {
	var b strings.Builder

	// Title
	b.WriteString(s.styles.Title.Render(s.title))
	b.WriteString("\n\n")

	// Options
	for i, opt := range s.options {
		cursor := "  "
		style := s.styles.Unselected
		symbol := "○"

		if i == s.cursor {
			cursor = ""
			style = s.styles.Selected
			symbol = "●"
		}

		// Option line
		b.WriteString(cursor)
		b.WriteString(style.Render(symbol + " " + opt.Label))
		b.WriteString("\n")

		// Description (if present)
		if opt.Description != "" {
			b.WriteString(s.styles.Description.Render(opt.Description))
			b.WriteString("\n")
		}
	}

	// Help
	if s.showHelp {
		b.WriteString(s.styles.Help.Render("\n↑/↓ navigate • enter select • q quit"))
	}

	return b.String()
}

// Selected returns the selected option index, or -1 if none selected.
func (s Selector) Selected() int {
	return s.selected
}

// SelectedOption returns the selected option, or nil if none selected.
func (s Selector) SelectedOption() *Option {
	if s.selected >= 0 && s.selected < len(s.options) {
		return &s.options[s.selected]
	}
	return nil
}

// Cancelled returns true if the user cancelled the selection.
func (s Selector) Cancelled() bool {
	return s.cancelled
}

// Submitted returns true if the user made a selection.
func (s Selector) Submitted() bool {
	return s.submitted
}

// Value returns the value of the selected option.
func (s Selector) Value() string {
	if opt := s.SelectedOption(); opt != nil {
		return opt.Value
	}
	return ""
}
