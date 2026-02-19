package components

import (
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Spinner is a loading indicator component.
type Spinner struct {
	spinner spinner.Model
	message string
	done    bool
	success bool
	result  string
	err     error
	styles  spinnerStyles
}

type spinnerStyles struct {
	Spinner lipgloss.Style
	Message lipgloss.Style
	Success lipgloss.Style
	Error   lipgloss.Style
}

func defaultSpinnerStyles() spinnerStyles {
	return spinnerStyles{
		Spinner: lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
		Message: lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		Success: lipgloss.NewStyle().Foreground(lipgloss.Color("34")),
		Error:   lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
	}
}

// NewSpinner creates a new spinner with the given message.
func NewSpinner(message string) Spinner {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))

	return Spinner{
		spinner: s,
		message: message,
		styles:  defaultSpinnerStyles(),
	}
}

// Init implements tea.Model.
func (s Spinner) Init() tea.Cmd {
	return s.spinner.Tick
}

// Update implements tea.Model.
func (s Spinner) Update(msg tea.Msg) (Spinner, tea.Cmd) {
	switch msg := msg.(type) {
	case SpinnerDoneMsg:
		s.done = true
		s.success = msg.Success
		s.result = msg.Result
		s.err = msg.Err
		return s, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	}
	return s, nil
}

// View implements tea.Model.
func (s Spinner) View() string {
	if s.done {
		if s.success {
			return s.styles.Success.Render("✓ " + s.result)
		}
		return s.styles.Error.Render("✗ " + s.err.Error())
	}
	return s.spinner.View() + " " + s.styles.Message.Render(s.message)
}

// SpinnerDoneMsg signals that the spinner operation is complete.
type SpinnerDoneMsg struct {
	Success bool
	Result  string
	Err     error
}

// Done creates a success message.
func SpinnerDone(result string) SpinnerDoneMsg {
	return SpinnerDoneMsg{Success: true, Result: result}
}

// SpinnerFailed creates a failure message.
func SpinnerFailed(err error) SpinnerDoneMsg {
	return SpinnerDoneMsg{Success: false, Err: err}
}

// SetMessage updates the spinner message.
func (s *Spinner) SetMessage(msg string) {
	s.message = msg
}

// IsDone returns true if the spinner is done.
func (s Spinner) IsDone() bool {
	return s.done
}

// IsSuccess returns true if the spinner completed successfully.
func (s Spinner) IsSuccess() bool {
	return s.success
}

// Error returns the error if the spinner failed.
func (s Spinner) Error() error {
	return s.err
}
