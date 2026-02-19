package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TextField is a labeled text input field.
type TextField struct {
	label       string
	placeholder string
	input       textinput.Model
	focused     bool
	width       int
	required    bool
	validator   func(string) error
	err         error
	styles      textFieldStyles
}

type textFieldStyles struct {
	Label        lipgloss.Style
	Input        lipgloss.Style
	FocusedInput lipgloss.Style
	Error        lipgloss.Style
	Required     lipgloss.Style
}

func defaultTextFieldStyles() textFieldStyles {
	return textFieldStyles{
		Label:        lipgloss.NewStyle().Foreground(lipgloss.Color("245")).MarginBottom(0),
		Input:        lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		FocusedInput: lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
		Error:        lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		Required:     lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
	}
}

// NewTextField creates a new text field.
func NewTextField(label, placeholder string) TextField {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = 256
	ti.Width = 40

	return TextField{
		label:       label,
		placeholder: placeholder,
		input:       ti,
		width:       50,
		styles:      defaultTextFieldStyles(),
	}
}

// WithWidth sets the width of the text field.
func (t TextField) WithWidth(width int) TextField {
	t.width = width
	t.input.Width = width - 4
	return t
}

// WithRequired marks the field as required.
func (t TextField) WithRequired(required bool) TextField {
	t.required = required
	return t
}

// WithValidator sets a validation function.
func (t TextField) WithValidator(fn func(string) error) TextField {
	t.validator = fn
	return t
}

// WithValue sets the initial value.
func (t TextField) WithValue(value string) TextField {
	t.input.SetValue(value)
	return t
}

// WithPassword configures the field as a password field.
func (t TextField) WithPassword() TextField {
	t.input.EchoMode = textinput.EchoPassword
	t.input.EchoCharacter = '•'
	return t
}

// Focus focuses the text field.
func (t *TextField) Focus() tea.Cmd {
	t.focused = true
	return t.input.Focus()
}

// Blur removes focus from the text field.
func (t *TextField) Blur() {
	t.focused = false
	t.input.Blur()
}

// IsFocused returns true if the field is focused.
func (t TextField) IsFocused() bool {
	return t.focused
}

// Init implements tea.Model.
func (t TextField) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (t TextField) Update(msg tea.Msg) (TextField, tea.Cmd) {
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)

	// Validate on change
	if t.validator != nil {
		t.err = t.validator(t.input.Value())
	}

	return t, cmd
}

// View implements tea.Model.
func (t TextField) View() string {
	var b strings.Builder

	// Label with required indicator
	labelText := t.label
	if t.required {
		labelText += t.styles.Required.Render(" *")
	}
	b.WriteString(t.styles.Label.Render(labelText))
	b.WriteString("\n")

	// Input field
	inputStyle := t.styles.Input
	if t.focused {
		inputStyle = t.styles.FocusedInput
	}

	// Render the input
	b.WriteString(inputStyle.Render(t.input.View()))

	// Error message
	if t.err != nil {
		b.WriteString("\n")
		b.WriteString(t.styles.Error.Render(t.err.Error()))
	}

	return b.String()
}

// Value returns the current value.
func (t TextField) Value() string {
	return t.input.Value()
}

// SetValue sets the value.
func (t *TextField) SetValue(v string) {
	t.input.SetValue(v)
}

// Error returns the current validation error.
func (t TextField) Error() error {
	return t.err
}

// Validate runs validation and returns any error.
func (t *TextField) Validate() error {
	if t.required && strings.TrimSpace(t.input.Value()) == "" {
		t.err = ErrFieldRequired
		return t.err
	}
	if t.validator != nil {
		t.err = t.validator(t.input.Value())
		return t.err
	}
	t.err = nil
	return nil
}

// ErrFieldRequired is returned when a required field is empty.
var ErrFieldRequired = fieldError("this field is required")

type fieldError string

func (e fieldError) Error() string { return string(e) }
