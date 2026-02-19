package wizards

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
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

	// Template selection
	templates   []TemplateInfo
	templateIdx int

	// Target directory
	targetDir string

	// Config setup choice
	setupConfig bool

	// Sub-wizards results
	connResult   ConnectionResult
	configResult ConfigResult

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
	initStepTemplate initStep = iota
	initStepSetupChoice
	initStepConnection
	initStepConfig
	initStepComplete
)

// NewInitWizard creates a new init wizard.
func NewInitWizard(targetDir string, templates []TemplateInfo) InitWizard {
	if targetDir == "" {
		targetDir = "."
	}
	return InitWizard{
		step:      initStepTemplate,
		targetDir: targetDir,
		templates: templates,
		width:     80,
		height:    24,
		styles:    defaultWizardStyles(),
		keys:      defaultWizardKeys(),
	}
}

// Init implements tea.Model.
func (w InitWizard) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (w InitWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		return w, nil

	case tea.KeyMsg:
		if key.Matches(msg, w.keys.Quit) {
			w.result.Cancelled = true
			return w, tea.Quit
		}

		switch w.step {
		case initStepTemplate:
			return w.updateTemplate(msg)
		case initStepSetupChoice:
			return w.updateSetupChoice(msg)
		case initStepComplete:
			return w.updateComplete(msg)
		}
	}

	return w, nil
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
		w.result.Cancelled = true
		return w, tea.Quit
	}
	return w, nil
}

func (w InitWizard) updateSetupChoice(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Up), key.Matches(msg, w.keys.Down):
		w.setupConfig = !w.setupConfig
	case key.Matches(msg, w.keys.Select):
		w.result.SetupConfig = w.setupConfig
		w.result.TargetDir = w.targetDir
		w.step = initStepComplete
		return w, tea.Quit
	case key.Matches(msg, w.keys.Back):
		w.step = initStepTemplate
	}
	return w, nil
}

func (w InitWizard) updateComplete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, w.keys.Select) {
		return w, tea.Quit
	}
	return w, nil
}

// View implements tea.Model.
func (w InitWizard) View() string {
	var b strings.Builder

	b.WriteString(w.styles.Title.Render("pgmi init - Project Setup"))
	b.WriteString("\n")

	switch w.step {
	case initStepTemplate:
		b.WriteString(w.viewTemplate())
	case initStepSetupChoice:
		b.WriteString(w.viewSetupChoice())
	case initStepComplete:
		b.WriteString(w.viewComplete())
	}

	return b.String()
}

func (w InitWizard) viewTemplate() string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render("Select a template"))
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

	b.WriteString(w.styles.Help.Render("\n↑/↓ navigate • enter select • q quit"))

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

func (w InitWizard) viewComplete() string {
	var b strings.Builder

	b.WriteString(w.styles.Success.Render("✓ Ready to create project"))
	b.WriteString("\n\n")

	absPath, _ := filepath.Abs(w.targetDir)
	b.WriteString(fmt.Sprintf("Directory: %s\n", absPath))
	b.WriteString(fmt.Sprintf("Template:  %s\n", w.result.Template))

	if w.result.SetupConfig {
		b.WriteString("\nAfter creation, you'll configure the database connection.\n")
	}

	b.WriteString(w.styles.Help.Render("\nenter create project • esc cancel"))

	return b.String()
}

// Result returns the wizard result.
func (w InitWizard) Result() InitResult {
	return w.result
}

// RunInitWizard executes the init wizard.
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

// ShowInitComplete displays the completion message after project creation.
func ShowInitComplete(targetDir string, template string, files []string) {
	absPath, _ := filepath.Abs(targetDir)

	fmt.Println()
	fmt.Println("✓ Project created successfully!")
	fmt.Println()
	fmt.Printf("%s/\n", absPath)

	for _, f := range files {
		rel, _ := filepath.Rel(targetDir, f)
		fmt.Printf("├── %s\n", rel)
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Printf("  1. cd %s\n", targetDir)
	fmt.Println("  2. Edit your SQL files")
	fmt.Println("  3. Run: pgmi deploy")
	fmt.Println()
}

// DirectoryExists checks if a directory exists and is not empty.
func DirectoryExists(path string) (bool, bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.IsDir() {
		return false, false, fmt.Errorf("path exists but is not a directory")
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return true, false, err
	}

	return true, len(entries) > 0, nil
}
