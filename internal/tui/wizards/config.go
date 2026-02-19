package wizards

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// ConfigResult holds the result of the config wizard.
type ConfigResult struct {
	Cancelled bool
	Config    config.ProjectConfig
	SavePath  string
}

// ConfigWizard guides users through creating pgmi.yaml.
type ConfigWizard struct {
	step configStep

	// Connection info (from connection wizard or existing)
	connConfig pgmi.ConnectionConfig
	hasConn    bool

	// Parameters
	params    []paramEntry
	paramIdx  int
	editingKV bool
	kvInputs  []textinput.Model
	kvFocus   int

	// Timeout
	timeout string

	// Result
	result ConfigResult

	// Dimensions
	width  int
	height int

	// Styles and keys
	styles wizardStyles
	keys   wizardKeys
}

type configStep int

const (
	configStepConnection configStep = iota
	configStepParams
	configStepTimeout
	configStepReview
	configStepDone
)

type paramEntry struct {
	Key   string
	Value string
}

// NewConfigWizard creates a new config wizard.
func NewConfigWizard() ConfigWizard {
	return ConfigWizard{
		step:    configStepConnection,
		params:  []paramEntry{},
		timeout: "3m",
		width:   80,
		height:  24,
		styles:  defaultWizardStyles(),
		keys:    defaultWizardKeys(),
	}
}

// WithConnection sets the connection config (from connection wizard).
func (w ConfigWizard) WithConnection(cfg pgmi.ConnectionConfig) ConfigWizard {
	w.connConfig = cfg
	w.hasConn = true
	w.step = configStepParams
	return w
}

// Init implements tea.Model.
func (w ConfigWizard) Init() tea.Cmd {
	if !w.hasConn {
		// Start connection wizard inline
		return nil
	}
	return nil
}

// Update implements tea.Model.
func (w ConfigWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		return w, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			w.result.Cancelled = true
			return w, tea.Quit
		}

		switch w.step {
		case configStepParams:
			return w.updateParams(msg)
		case configStepTimeout:
			return w.updateTimeout(msg)
		case configStepReview:
			return w.updateReview(msg)
		}
	}

	return w, nil
}

func (w ConfigWizard) updateParams(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if w.editingKV {
		return w.updateKVEdit(msg)
	}

	switch {
	case key.Matches(msg, w.keys.Up):
		if w.paramIdx > 0 {
			w.paramIdx--
		}
	case key.Matches(msg, w.keys.Down):
		if w.paramIdx < len(w.params) {
			w.paramIdx++
		}
	case key.Matches(msg, w.keys.Select):
		if w.paramIdx == len(w.params) {
			// Add new parameter
			w.editingKV = true
			w.kvInputs = w.createKVInputs("", "")
			w.kvFocus = 0
			return w, w.kvInputs[0].Focus()
		} else {
			// Edit existing
			p := w.params[w.paramIdx]
			w.editingKV = true
			w.kvInputs = w.createKVInputs(p.Key, p.Value)
			w.kvFocus = 0
			return w, w.kvInputs[0].Focus()
		}
	case msg.String() == "d":
		// Delete parameter
		if w.paramIdx < len(w.params) {
			w.params = append(w.params[:w.paramIdx], w.params[w.paramIdx+1:]...)
			if w.paramIdx > 0 && w.paramIdx >= len(w.params) {
				w.paramIdx--
			}
		}
	case msg.String() == "n":
		// Next step
		w.step = configStepTimeout
	case key.Matches(msg, w.keys.Back):
		w.result.Cancelled = true
		return w, tea.Quit
	}
	return w, nil
}

func (w *ConfigWizard) createKVInputs(k, v string) []textinput.Model {
	keyInput := textinput.New()
	keyInput.Placeholder = "parameter_name"
	keyInput.CharLimit = 64
	keyInput.Width = 30
	keyInput.SetValue(k)

	valInput := textinput.New()
	valInput.Placeholder = "value"
	valInput.CharLimit = 256
	valInput.Width = 40
	valInput.SetValue(v)

	return []textinput.Model{keyInput, valInput}
}

func (w ConfigWizard) updateKVEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Tab), msg.String() == "down":
		if w.kvFocus < len(w.kvInputs)-1 {
			w.kvInputs[w.kvFocus].Blur()
			w.kvFocus++
			return w, w.kvInputs[w.kvFocus].Focus()
		}
	case msg.String() == "shift+tab", msg.String() == "up":
		if w.kvFocus > 0 {
			w.kvInputs[w.kvFocus].Blur()
			w.kvFocus--
			return w, w.kvInputs[w.kvFocus].Focus()
		}
	case key.Matches(msg, w.keys.Select):
		// Save the parameter
		k := w.kvInputs[0].Value()
		v := w.kvInputs[1].Value()
		if k != "" {
			if w.paramIdx < len(w.params) {
				// Update existing
				w.params[w.paramIdx] = paramEntry{Key: k, Value: v}
			} else {
				// Add new
				w.params = append(w.params, paramEntry{Key: k, Value: v})
			}
		}
		w.editingKV = false
		w.kvInputs = nil
		return w, nil
	case key.Matches(msg, w.keys.Back):
		w.editingKV = false
		w.kvInputs = nil
		return w, nil
	default:
		var cmd tea.Cmd
		w.kvInputs[w.kvFocus], cmd = w.kvInputs[w.kvFocus].Update(msg)
		return w, cmd
	}
	return w, nil
}

func (w ConfigWizard) updateTimeout(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "1":
		w.timeout = "1m"
	case "3":
		w.timeout = "3m"
	case "5":
		w.timeout = "5m"
	case "0":
		w.timeout = "10m"
	}

	switch {
	case key.Matches(msg, w.keys.Select), msg.String() == "n":
		w.step = configStepReview
	case key.Matches(msg, w.keys.Back):
		w.step = configStepParams
	}
	return w, nil
}

func (w ConfigWizard) updateReview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Select):
		w.buildConfig()
		w.step = configStepDone
		return w, tea.Quit
	case key.Matches(msg, w.keys.Back):
		w.step = configStepTimeout
	}
	return w, nil
}

func (w *ConfigWizard) buildConfig() {
	cfg := config.ProjectConfig{
		Connection: config.ConnectionConfig{
			Host:     w.connConfig.Host,
			Port:     w.connConfig.Port,
			Username: w.connConfig.Username,
			Database: w.connConfig.Database,
			SSLMode:  w.connConfig.SSLMode,
		},
		Params:  make(map[string]string),
		Timeout: w.timeout,
	}

	for _, p := range w.params {
		cfg.Params[p.Key] = p.Value
	}

	w.result.Config = cfg
	w.result.SavePath = "pgmi.yaml"
}

// View implements tea.Model.
func (w ConfigWizard) View() string {
	var b strings.Builder

	b.WriteString(w.styles.Title.Render("pgmi - Configuration Builder"))
	b.WriteString("\n")

	switch w.step {
	case configStepParams:
		b.WriteString(w.viewParams())
	case configStepTimeout:
		b.WriteString(w.viewTimeout())
	case configStepReview:
		b.WriteString(w.viewReview())
	}

	return b.String()
}

func (w ConfigWizard) viewParams() string {
	var b strings.Builder

	if w.hasConn {
		b.WriteString(w.styles.Success.Render("✓ Connection: "))
		b.WriteString(fmt.Sprintf("%s:%d/%s", w.connConfig.Host, w.connConfig.Port, w.connConfig.Database))
		b.WriteString("\n\n")
	}

	b.WriteString(w.styles.Subtitle.Render("Parameters"))
	b.WriteString("\n")
	b.WriteString(w.styles.Description.Render("Add key-value parameters for deploy.sql (optional)"))
	b.WriteString("\n\n")

	if w.editingKV {
		b.WriteString("Key:   ")
		b.WriteString(w.kvInputs[0].View())
		b.WriteString("\n")
		b.WriteString("Value: ")
		b.WriteString(w.kvInputs[1].View())
		b.WriteString("\n\n")
		b.WriteString(w.styles.Help.Render("tab next • enter save • esc cancel"))
	} else {
		// Show existing params
		for i, p := range w.params {
			cursor := "  "
			style := w.styles.Unselected
			if i == w.paramIdx {
				cursor = ""
				style = w.styles.Selected
			}
			b.WriteString(cursor)
			b.WriteString(style.Render(fmt.Sprintf("%s = %s", p.Key, p.Value)))
			b.WriteString("\n")
		}

		// Add new option
		cursor := "  "
		style := w.styles.Unselected
		if w.paramIdx == len(w.params) {
			cursor = ""
			style = w.styles.Selected
		}
		b.WriteString(cursor)
		b.WriteString(style.Render("+ Add parameter"))
		b.WriteString("\n\n")

		b.WriteString(w.styles.Help.Render("↑/↓ navigate • enter edit • d delete • n next step"))
	}

	return b.String()
}

func (w ConfigWizard) viewTimeout() string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render("Timeout"))
	b.WriteString("\n")
	b.WriteString(w.styles.Description.Render("Maximum time for deployment (press 1, 3, 5, or 0 for 10m)"))
	b.WriteString("\n\n")

	timeouts := []string{"1m", "3m", "5m", "10m"}
	for _, t := range timeouts {
		style := w.styles.Unselected
		symbol := "○"
		if t == w.timeout {
			style = w.styles.Selected
			symbol = "●"
		}
		b.WriteString("  ")
		b.WriteString(style.Render(symbol + " " + t))
		b.WriteString("\n")
	}

	b.WriteString(w.styles.Help.Render("\n1/3/5/0 select • n next step • esc back"))

	return b.String()
}

func (w ConfigWizard) viewReview() string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render("Review Configuration"))
	b.WriteString("\n\n")

	// Build preview YAML
	cfg := config.ProjectConfig{
		Connection: config.ConnectionConfig{
			Host:     w.connConfig.Host,
			Port:     w.connConfig.Port,
			Username: w.connConfig.Username,
			Database: w.connConfig.Database,
			SSLMode:  w.connConfig.SSLMode,
		},
		Params:  make(map[string]string),
		Timeout: w.timeout,
	}
	for _, p := range w.params {
		cfg.Params[p.Key] = p.Value
	}

	yamlBytes, _ := yaml.Marshal(cfg)
	lines := strings.Split(string(yamlBytes), "\n")
	for _, line := range lines {
		b.WriteString(w.styles.Description.Render("  " + line))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(w.styles.Help.Render("enter save to pgmi.yaml • esc go back"))

	return b.String()
}

// Result returns the wizard result.
func (w ConfigWizard) Result() ConfigResult {
	return w.result
}

// SaveConfig saves the configuration to pgmi.yaml.
func (w ConfigWizard) SaveConfig(dir string) error {
	path := filepath.Join(dir, "pgmi.yaml")

	data, err := yaml.Marshal(w.result.Config)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// RunConfigWizard executes the config wizard with an existing connection.
func RunConfigWizard(connConfig pgmi.ConnectionConfig) (ConfigResult, error) {
	wizard := NewConfigWizard().WithConnection(connConfig)
	p := tea.NewProgram(wizard, tea.WithAltScreen())

	model, err := p.Run()
	if err != nil {
		return ConfigResult{Cancelled: true}, err
	}

	return model.(ConfigWizard).Result(), nil
}
