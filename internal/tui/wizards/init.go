package wizards

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vvka-141/pgmi/internal/tui/components"
)

// TemplateInfo holds template metadata for display.
type TemplateInfo struct {
	Name        string
	Description string
}

// DefaultTemplates returns the available template information.
func DefaultTemplates() []TemplateInfo {
	return []TemplateInfo{
		{Name: "basic", Description: "Simple migrations with sequential execution"},
		{Name: "advanced", Description: "Production-ready with metadata-driven deployment"},
	}
}

// pgmiManagedInitFiles are files that pgmi creates/manages and don't block init.
var pgmiManagedInitFiles = map[string]bool{
	"pgmi.yaml": true,
	".env":      true,
}

// InitResult holds the result of the init wizard.
type InitResult struct {
	Cancelled    bool
	TargetDir    string
	Template     string
	SetupConfig  bool
	ConfigResult ConfigResult
	ConnResult   ConnectionResult
}

// InitWizard guides users through project initialization.
type InitWizard struct {
	step initStep

	// Directory input
	dirInput    textinput.Model
	dirError    string
	dirComplete *components.PathCompleter

	// Template selection
	templates   []TemplateInfo
	templateIdx int

	// Config setup choice
	setupConfig bool

	// Result
	result InitResult

	// Dimensions
	width  int
	height int

	// Styles and keys
	styles wizardStyles
	keys   wizardKeys
}

type initStep int

const (
	initStepDirectory initStep = iota
	initStepTemplate
	initStepSetupChoice
)

// NewInitWizard creates a new init wizard.
// If targetDir is non-empty, the directory step is pre-filled but still shown for confirmation.
func NewInitWizard(targetDir string, templates []TemplateInfo) InitWizard {
	di := textinput.New()
	di.Placeholder = "."
	di.CharLimit = 256
	di.Width = 50
	if targetDir != "" {
		di.SetValue(targetDir)
	}
	di.Focus() // Must focus here — Init() has value receiver, state changes there are lost

	return InitWizard{
		step:        initStepDirectory,
		dirInput:    di,
		dirComplete: components.NewPathCompleter(true),
		templates:   templates,
		width:       80,
		height:      24,
		styles:      defaultWizardStyles(),
		keys:        defaultWizardKeys(),
	}
}

// Init implements tea.Model.
func (w InitWizard) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (w InitWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		return w, nil

	case tea.KeyMsg:
		// ctrl+c always quits
		if msg.String() == "ctrl+c" {
			w.result.Cancelled = true
			return w, tea.Quit
		}

		switch w.step {
		case initStepDirectory:
			return w.updateDirectory(msg)
		case initStepTemplate:
			return w.updateTemplate(msg)
		case initStepSetupChoice:
			return w.updateSetupChoice(msg)
		}

	default:
		// Forward non-key messages (e.g. focus, blink) to the active text input
		if w.step == initStepDirectory {
			var cmd tea.Cmd
			w.dirInput, cmd = w.dirInput.Update(msg)
			return w, cmd
		}
	}

	return w, nil
}

func (w InitWizard) resolveDir() string {
	dir := w.dirInput.Value()
	if dir == "" {
		dir = "."
	}
	return dir
}

func (w InitWizard) updateDirectory(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Select):
		dir := w.resolveDir()

		// Validate: check for blocking files
		if blocking := checkDirBlocking(dir); len(blocking) > 0 {
			absPath, _ := filepath.Abs(dir)
			w.dirError = fmt.Sprintf("'%s' contains: %s", absPath, strings.Join(blocking, ", "))
			return w, nil
		}

		w.dirError = ""
		w.result.TargetDir = dir
		w.step = initStepTemplate
		return w, nil

	case msg.String() == "tab":
		completed := w.dirComplete.Next(w.dirInput.Value())
		if completed != w.dirInput.Value() {
			w.dirInput.SetValue(completed)
			w.dirInput.CursorEnd()
			w.dirError = ""
		}
		return w, nil

	case key.Matches(msg, w.keys.Back):
		w.result.Cancelled = true
		return w, tea.Quit

	default:
		w.dirComplete.Reset()
		w.dirError = ""
		var cmd tea.Cmd
		w.dirInput, cmd = w.dirInput.Update(msg)
		return w, cmd
	}
}

// checkDirBlocking returns the list of non-pgmi files blocking init, or nil if safe.
func checkDirBlocking(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // doesn't exist yet — fine
	}

	var blocking []string
	for _, entry := range entries {
		if !pgmiManagedInitFiles[entry.Name()] {
			blocking = append(blocking, entry.Name())
		}
	}
	return blocking
}

func (w InitWizard) updateTemplate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Up):
		if w.templateIdx > 0 {
			w.templateIdx--
		}
	case key.Matches(msg, w.keys.Down):
		if w.templateIdx < len(w.templates)-1 {
			w.templateIdx++
		}
	case key.Matches(msg, w.keys.Select):
		w.result.Template = w.templates[w.templateIdx].Name
		w.step = initStepSetupChoice
	case key.Matches(msg, w.keys.Back):
		w.step = initStepDirectory
		return w, w.dirInput.Focus()
	}
	return w, nil
}

func (w InitWizard) updateSetupChoice(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Up), key.Matches(msg, w.keys.Down):
		w.setupConfig = !w.setupConfig
	case key.Matches(msg, w.keys.Select):
		w.result.SetupConfig = w.setupConfig
		return w, tea.Quit
	case key.Matches(msg, w.keys.Back):
		w.step = initStepTemplate
	}
	return w, nil
}

// View implements tea.Model.
func (w InitWizard) View() string {
	var b strings.Builder

	b.WriteString(w.styles.Title.Render("pgmi init - Project Setup"))
	b.WriteString("\n")

	switch w.step {
	case initStepDirectory:
		b.WriteString(w.viewDirectory())
	case initStepTemplate:
		b.WriteString(w.viewTemplate())
	case initStepSetupChoice:
		b.WriteString(w.viewSetupChoice())
	}

	return b.String()
}

func (w InitWizard) viewDirectory() string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render("Where do you want to create the project?"))
	b.WriteString("\n\n")

	b.WriteString(w.styles.Label.Render("Directory:"))
	b.WriteString("\n")
	b.WriteString(w.styles.FocusedBox.Render(w.dirInput.View()))
	b.WriteString("\n")

	if w.dirError != "" {
		b.WriteString("\n")
		b.WriteString(w.styles.Error.Render("Directory is not empty: " + w.dirError))
		b.WriteString("\n")
		b.WriteString(w.styles.Description.Render("Choose a different location or remove existing files."))
		b.WriteString("\n")
		b.WriteString(w.styles.Description.Render("pgmi.yaml and .env are allowed."))
	} else {
		b.WriteString(w.styles.Description.Render("\nPress enter for current directory, or type a path."))
	}

	b.WriteString(w.styles.Help.Render("\n\nenter confirm • esc cancel"))

	return b.String()
}

func (w InitWizard) viewTemplate() string {
	var b strings.Builder

	absPath, _ := filepath.Abs(w.result.TargetDir)
	b.WriteString(w.styles.Subtitle.Render(fmt.Sprintf("Select a template  →  %s", absPath)))
	b.WriteString("\n\n")

	for i, t := range w.templates {
		cursor := "  "
		style := w.styles.Unselected
		symbol := "○"

		if i == w.templateIdx {
			cursor = ""
			style = w.styles.Selected
			symbol = "●"
		}

		b.WriteString(cursor)
		b.WriteString(style.Render(symbol + " " + t.Name))
		b.WriteString("\n")
		b.WriteString(w.styles.Description.Render(t.Description))
		b.WriteString("\n")
	}

	b.WriteString(w.styles.Help.Render("\n↑/↓ navigate • enter select • esc back"))

	return b.String()
}

func (w InitWizard) viewSetupChoice() string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render("Configure database connection now?"))
	b.WriteString("\n\n")

	options := []struct {
		selected bool
		name     string
		desc     string
	}{
		{!w.setupConfig, "No, I'll configure later", "Creates project with placeholder pgmi.yaml"},
		{w.setupConfig, "Yes, set up connection (recommended)", "Configure pgmi.yaml with your database settings"},
	}

	for _, opt := range options {
		cursor := "  "
		style := w.styles.Unselected
		symbol := "○"

		if opt.selected {
			cursor = ""
			style = w.styles.Selected
			symbol = "●"
		}

		b.WriteString(cursor)
		b.WriteString(style.Render(symbol + " " + opt.name))
		b.WriteString("\n")
		b.WriteString(w.styles.Description.Render(opt.desc))
		b.WriteString("\n")
	}

	b.WriteString(w.styles.Help.Render("\n↑/↓ toggle • enter select • esc back"))

	return b.String()
}

// Result returns the wizard result.
func (w InitWizard) Result() InitResult {
	return w.result
}

// RunInitWizard executes the init wizard.
// targetDir is used as pre-filled value (empty string shows placeholder ".").
func RunInitWizard(targetDir string) (InitResult, error) {
	templates := DefaultTemplates()
	if len(templates) == 0 {
		return InitResult{Cancelled: true}, fmt.Errorf("no templates available")
	}

	wizard := NewInitWizard(targetDir, templates)
	p := tea.NewProgram(wizard, tea.WithAltScreen())

	model, err := p.Run()
	if err != nil {
		return InitResult{Cancelled: true}, err
	}

	result := model.(InitWizard).Result()

	// If user wants to setup config, run connection wizard after project creation
	if result.SetupConfig && !result.Cancelled {
		connResult, err := RunConnectionWizard()
		if err != nil {
			return result, err
		}
		result.ConnResult = connResult

		if !connResult.Cancelled {
			cfgResult, err := RunConfigWizard(connResult.Config)
			if err != nil {
				return result, err
			}
			result.ConfigResult = cfgResult
		}
	}

	return result, nil
}
