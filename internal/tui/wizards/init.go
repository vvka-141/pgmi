package wizards

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/vvka-141/pgmi/internal/scaffold"
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
		{Name: "minimal", Description: scaffold.TemplateSummary("minimal")},
		{Name: "basic", Description: scaffold.TemplateSummary("basic")},
		{Name: "advanced", Description: scaffold.TemplateSummary("advanced")},
	}
}

// InitResult holds the result of the init wizard.
type InitResult struct {
	Cancelled   bool
	Interrupted bool
	TargetDir   string
	Template    string
	SetupConfig bool
	ConnResult  ConnectionResult
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

	// Embedded connection wizard (active when user chose to configure connection)
	connWizard *ConnectionWizard
	connActive bool

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

// NewInitWizard creates a new init wizard. A targetDir the user already named
// skips the directory step unless something in it blocks init; then the step
// shows why.
func NewInitWizard(targetDir string, templates []TemplateInfo) InitWizard {
	di := textinput.New()
	di.Placeholder = "."
	di.CharLimit = 256
	di.SetWidth(50)
	if targetDir != "" {
		di.SetValue(targetDir)
	}
	di.Focus() // Must focus here — Init() has value receiver, state changes there are lost

	w := InitWizard{
		step:        initStepDirectory,
		dirInput:    di,
		dirComplete: components.NewPathCompleter(true),
		templates:   templates,
		width:       80,
		height:      24,
		styles:      defaultWizardStyles(),
		keys:        defaultWizardKeys(),
	}
	if targetDir != "" {
		if blocking := checkDirBlocking(targetDir); len(blocking) > 0 {
			absPath, _ := filepath.Abs(targetDir)
			w.dirError = fmt.Sprintf("'%s' contains: %s", absPath, strings.Join(blocking, ", "))
		} else {
			w.result.TargetDir = targetDir
			w.step = initStepTemplate
		}
	}
	return w
}

// Init implements tea.Model.
func (w InitWizard) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (w InitWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if w.connActive && w.connWizard != nil {
		m, cmd := w.connWizard.Update(msg)
		cw := m.(ConnectionWizard)
		w.connWizard = &cw
		switch {
		case cw.result.Interrupted:
			w.result.Cancelled = true
			w.result.Interrupted = true
			return w, tea.Quit
		case cw.result.Cancelled:
			w.connActive = false
			w.connWizard = nil
			w.step = initStepSetupChoice
			return w, nil
		case cw.step == stepDone:
			w.result.ConnResult = cw.Result()
			return w, tea.Quit
		}
		return w, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		return w, nil

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			w.result.Cancelled = true
			w.result.Interrupted = true
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

func (w InitWizard) updateDirectory(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
	return scaffold.BlockingEntries(dir)
}

func (w InitWizard) updateTemplate(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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

func (w InitWizard) updateSetupChoice(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Up), key.Matches(msg, w.keys.Down):
		w.setupConfig = !w.setupConfig
	case key.Matches(msg, w.keys.Select):
		w.result.SetupConfig = w.setupConfig
		if w.setupConfig {
			cw := NewConnectionWizard()
			cw.width, cw.height = w.width, w.height
			w.connWizard = &cw
			w.connActive = true
			return w, cw.Init()
		}
		return w, tea.Quit
	case key.Matches(msg, w.keys.Back):
		w.step = initStepTemplate
	}
	return w, nil
}

// View implements tea.Model.
func (w InitWizard) View() tea.View {
	v := tea.NewView(w.content())
	v.AltScreen = true
	return v
}

func (w InitWizard) content() string {
	if w.connActive && w.connWizard != nil {
		return w.connWizard.content()
	}
	w.styles = w.styles.sized(w.width)

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
		b.WriteString(w.styles.Description.Render("Pick another path or remove the files (pgmi.yaml, .env and .git are tolerated)."))
	} else {
		b.WriteString("\n" + w.styles.Description.Render("Enter for current directory, or type a path."))
	}

	b.WriteString("\n" + w.styles.Help.Render("\nenter confirm • esc cancel"))

	return b.String()
}

func (w InitWizard) viewTemplate() string {
	var b strings.Builder

	absPath, _ := filepath.Abs(w.result.TargetDir)
	b.WriteString(w.styles.Subtitle.Render(fmt.Sprintf("Select a template  →  %s", absPath)))
	b.WriteString("\n\n")

	for i, t := range w.templates {
		style := w.styles.Unselected
		symbol := "○"

		if i == w.templateIdx {
			style = w.styles.Selected
			symbol = "●"
		}

		b.WriteString("  ")
		b.WriteString(style.Render(symbol + " " + t.Name))
		b.WriteString("\n")
		b.WriteString(w.styles.Description.Render(t.Description))
		b.WriteString("\n")
	}

	b.WriteString(w.styles.Help.Render("\n↑/↓ navigate • enter select • esc back"))

	return b.String()
}

// noSetupNote says what declining leaves behind: minimal writes no pgmi.yaml.
func (w InitWizard) noSetupNote() string {
	if w.result.Template == "minimal" {
		return "no pgmi.yaml; pass the connection on the command line"
	}
	return "pgmi.yaml gets a placeholder"
}

func (w InitWizard) viewSetupChoice() string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render("Set up the connection now?"))
	b.WriteString("\n\n")

	options := []struct {
		selected bool
		name     string
		desc     string
	}{
		{!w.setupConfig, "No — configure later", w.noSetupNote()},
		{w.setupConfig, "Yes — run the connection wizard", "writes connection settings to pgmi.yaml"},
	}

	for _, opt := range options {
		style := w.styles.Unselected
		symbol := "○"

		if opt.selected {
			style = w.styles.Selected
			symbol = "●"
		}

		b.WriteString("  ")
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
	p := tea.NewProgram(wizard)

	model, err := p.Run()
	if err != nil {
		return InitResult{Cancelled: true}, err
	}

	return model.(InitWizard).Result(), nil
}
