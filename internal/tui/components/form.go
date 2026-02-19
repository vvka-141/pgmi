package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Form is a component for collecting multiple text fields.
type Form struct {
	title     string
	fields    []TextField
	focusIdx  int
	width     int
	submitted bool
	cancelled bool
	keyMap    formKeyMap
	styles    formStyles
}

type formKeyMap struct {
	Next   key.Binding
	Prev   key.Binding
	Submit key.Binding
	Cancel key.Binding
}

type formStyles struct {
	Title lipgloss.Style
	Help  lipgloss.Style
}

func defaultFormKeyMap() formKeyMap {
	return formKeyMap{
		Next: key.NewBinding(
			key.WithKeys("tab", "down"),
			key.WithHelp("tab/↓", "next"),
		),
		Prev: key.NewBinding(
			key.WithKeys("shift+tab", "up"),
			key.WithHelp("shift+tab/↑", "prev"),
		),
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "submit"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc", "ctrl+c"),
			key.WithHelp("esc", "cancel"),
		),
	}
}

func defaultFormStyles() formStyles {
	return formStyles{
		Title: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).MarginBottom(1),
		Help:  lipgloss.NewStyle().Foreground(lipgloss.Color("240")).MarginTop(1),
	}
}

// NewForm creates a new form with the given title and fields.
func NewForm(title string, fields ...TextField) Form {
	return Form{
		title:  title,
		fields: fields,
		width:  60,
		keyMap: defaultFormKeyMap(),
		styles: defaultFormStyles(),
	}
}

// WithWidth sets the form width.
func (f Form) WithWidth(width int) Form {
	f.width = width
	return f
}

// Init implements tea.Model.
func (f Form) Init() tea.Cmd {
	if len(f.fields) > 0 {
		return f.fields[0].Focus()
	}
	return nil
}

// Update implements tea.Model.
func (f Form) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, f.keyMap.Next):
			return f.nextField()
		case key.Matches(msg, f.keyMap.Prev):
			return f.prevField()
		case key.Matches(msg, f.keyMap.Submit):
			// Only submit if on last field and all valid
			if f.focusIdx == len(f.fields)-1 {
				if f.validate() {
					f.submitted = true
					return f, tea.Quit
				}
			} else {
				return f.nextField()
			}
		case key.Matches(msg, f.keyMap.Cancel):
			f.cancelled = true
			return f, tea.Quit
		}
	case tea.WindowSizeMsg:
		f.width = msg.Width
	}

	// Update focused field
	if f.focusIdx < len(f.fields) {
		var cmd tea.Cmd
		f.fields[f.focusIdx], cmd = f.fields[f.focusIdx].Update(msg)
		cmds = append(cmds, cmd)
	}

	return f, tea.Batch(cmds...)
}

func (f Form) nextField() (tea.Model, tea.Cmd) {
	// Validate current field before moving
	if f.focusIdx < len(f.fields) {
		if err := f.fields[f.focusIdx].Validate(); err != nil {
			return f, nil
		}
	}

	if f.focusIdx < len(f.fields)-1 {
		f.fields[f.focusIdx].Blur()
		f.focusIdx++
		return f, f.fields[f.focusIdx].Focus()
	}
	return f, nil
}

func (f Form) prevField() (tea.Model, tea.Cmd) {
	if f.focusIdx > 0 {
		f.fields[f.focusIdx].Blur()
		f.focusIdx--
		return f, f.fields[f.focusIdx].Focus()
	}
	return f, nil
}

func (f *Form) validate() bool {
	valid := true
	for i := range f.fields {
		if err := f.fields[i].Validate(); err != nil {
			valid = false
		}
	}
	return valid
}

// View implements tea.Model.
func (f Form) View() string {
	var b strings.Builder

	// Title
	b.WriteString(f.styles.Title.Render(f.title))
	b.WriteString("\n\n")

	// Fields
	for i, field := range f.fields {
		b.WriteString(field.View())
		if i < len(f.fields)-1 {
			b.WriteString("\n\n")
		}
	}

	// Help
	b.WriteString(f.styles.Help.Render("\ntab next • shift+tab prev • enter submit • esc cancel"))

	return b.String()
}

// Submitted returns true if the form was submitted.
func (f Form) Submitted() bool {
	return f.submitted
}

// Cancelled returns true if the form was cancelled.
func (f Form) Cancelled() bool {
	return f.cancelled
}

// Values returns a map of field labels to values.
func (f Form) Values() map[string]string {
	result := make(map[string]string)
	for _, field := range f.fields {
		result[field.label] = field.Value()
	}
	return result
}

// Field returns a field by index.
func (f Form) Field(idx int) *TextField {
	if idx >= 0 && idx < len(f.fields) {
		return &f.fields[idx]
	}
	return nil
}

// FieldValue returns the value of a field by index.
func (f Form) FieldValue(idx int) string {
	if field := f.Field(idx); field != nil {
		return field.Value()
	}
	return ""
}
