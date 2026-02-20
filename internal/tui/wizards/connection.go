package wizards

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5"

	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// ConnectionTester tests database connectivity.
type ConnectionTester interface {
	TestConnection(ctx context.Context, cfg pgmi.ConnectionConfig) (info string, err error)
}

type pgxTester struct{}

func (pgxTester) TestConnection(ctx context.Context, cfg pgmi.ConnectionConfig) (string, error) {
	if cfg.AuthMethod != pgmi.AuthMethodStandard {
		return fmt.Sprintf("Configuration ready for %s authentication", cfg.AuthMethod.String()), nil
	}

	var connStr string
	if cs, ok := cfg.AdditionalParams["connection_string"]; ok && cs != "" {
		connStr = cs
	} else {
		connStr = db.BuildConnectionString(&cfg)
	}

	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)

	var version string
	if err := conn.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		return "", err
	}

	if idx := strings.Index(version, ","); idx > 0 {
		version = version[:idx]
	}
	return version, nil
}

// WizardOption configures a ConnectionWizard.
type WizardOption func(*ConnectionWizard)

// WithTester injects a ConnectionTester (for testing/mocking).
func WithTester(t ConnectionTester) WizardOption {
	return func(w *ConnectionWizard) {
		w.tester = t
	}
}

// Provider IDs.
const (
	providerLocal  = "local"
	providerAzure  = "azure"
	providerAWS    = "aws"
	providerGoogle = "google"
	providerCustom = "custom"
)

// Auth method IDs.
const (
	authPassword   = "password"
	authEntra      = "entra"
	authIAM        = "iam"
	authConnString = "connstring"
)

// ConnectionResult holds the result of the connection wizard.
type ConnectionResult struct {
	Cancelled          bool
	Config             pgmi.ConnectionConfig
	Tested             bool
	ManagementDatabase string
}

// Provider represents a database hosting provider.
type Provider struct {
	ID          string
	Name        string
	Description string
	AuthMethods []AuthOption
}

// AuthOption represents an authentication method.
type AuthOption struct {
	ID          string
	Name        string
	Description string
	AuthMethod  pgmi.AuthMethod
}

// Available providers.
var providers = []Provider{
	{
		ID:          providerLocal,
		Name:        "Local / On-Premises",
		Description: "PostgreSQL on localhost or your own servers",
		AuthMethods: []AuthOption{
			{ID: authPassword, Name: "Username and Password", Description: "Standard PostgreSQL authentication", AuthMethod: pgmi.AuthMethodStandard},
		},
	},
	{
		ID:          providerAzure,
		Name:        "Azure Database for PostgreSQL",
		Description: "Microsoft Azure managed PostgreSQL",
		AuthMethods: []AuthOption{
			{ID: authEntra, Name: "Azure Entra ID (Recommended)", Description: "Uses az login, managed identity, or environment variables", AuthMethod: pgmi.AuthMethodAzureEntraID},
			{ID: authPassword, Name: "Username and Password", Description: "Standard PostgreSQL authentication", AuthMethod: pgmi.AuthMethodStandard},
		},
	},
	{
		ID:          providerAWS,
		Name:        "AWS RDS PostgreSQL",
		Description: "Amazon Web Services managed PostgreSQL",
		AuthMethods: []AuthOption{
			{ID: authIAM, Name: "IAM Database Authentication", Description: "Uses AWS credentials for authentication", AuthMethod: pgmi.AuthMethodAWSIAM},
			{ID: authPassword, Name: "Username and Password", Description: "Standard PostgreSQL authentication", AuthMethod: pgmi.AuthMethodStandard},
		},
	},
	{
		ID:          providerGoogle,
		Name:        "Google Cloud SQL",
		Description: "Google Cloud managed PostgreSQL",
		AuthMethods: []AuthOption{
			{ID: authIAM, Name: "Cloud SQL IAM", Description: "Uses Google Cloud credentials", AuthMethod: pgmi.AuthMethodGoogleIAM},
			{ID: authPassword, Name: "Username and Password", Description: "Standard PostgreSQL authentication", AuthMethod: pgmi.AuthMethodStandard},
		},
	},
	{
		ID:          providerCustom,
		Name:        "Other / Connection String",
		Description: "Enter a full PostgreSQL connection string",
		AuthMethods: []AuthOption{
			{ID: authConnString, Name: "Connection String", Description: "postgresql://user:pass@host:port/database", AuthMethod: pgmi.AuthMethodStandard},
		},
	},
}

// ConnectionWizard guides users through setting up a database connection.
type ConnectionWizard struct {
	// Current step
	step wizardStep

	// Provider selection
	providerIdx int
	provider    *Provider

	// Auth method selection
	authIdx    int
	authMethod *AuthOption

	// Form inputs
	inputs        []textinput.Model
	focusIndex    int
	validationErr string

	// Connection testing
	spinner  spinner.Model
	testing  bool
	testDone bool
	testOK   bool
	testErr  error
	testInfo string

	// Result
	result ConnectionResult

	// Dimensions
	width  int
	height int

	// Styles
	styles wizardStyles

	// Key bindings
	keys wizardKeys

	// Connection tester (injectable for testing)
	tester ConnectionTester
}

type wizardStep int

const (
	stepSelectProvider wizardStep = iota
	stepSelectAuth
	stepInputHost
	stepInputAzure
	stepInputAWS
	stepInputGoogle
	stepInputConnString
	stepTestConnection
	stepDone
)

type wizardStyles struct {
	Title       lipgloss.Style
	Subtitle    lipgloss.Style
	Selected    lipgloss.Style
	Unselected  lipgloss.Style
	Description lipgloss.Style
	Help        lipgloss.Style
	Success     lipgloss.Style
	Error       lipgloss.Style
	Box         lipgloss.Style
	Label       lipgloss.Style
	FocusedBox  lipgloss.Style
}

type wizardKeys struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Back   key.Binding
	Quit   key.Binding
	Tab    key.Binding
}

func defaultWizardStyles() wizardStyles {
	return wizardStyles{
		Title:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).MarginBottom(1),
		Subtitle:    lipgloss.NewStyle().Foreground(lipgloss.Color("245")).MarginBottom(1),
		Selected:    lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true),
		Unselected:  lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		Description: lipgloss.NewStyle().Foreground(lipgloss.Color("240")).MarginLeft(4),
		Help:        lipgloss.NewStyle().Foreground(lipgloss.Color("240")).MarginTop(1),
		Success:     lipgloss.NewStyle().Foreground(lipgloss.Color("34")),
		Error:       lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		Box:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1),
		Label:       lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		FocusedBox:  lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39")).Padding(0, 1),
	}
}

func defaultWizardKeys() wizardKeys {
	return wizardKeys{
		Up:     key.NewBinding(key.WithKeys("up", "k")),
		Down:   key.NewBinding(key.WithKeys("down", "j")),
		Select: key.NewBinding(key.WithKeys("enter")),
		Back:   key.NewBinding(key.WithKeys("esc")),
		Quit:   key.NewBinding(key.WithKeys("ctrl+c", "q")),
		Tab:    key.NewBinding(key.WithKeys("tab")),
	}
}

// NewConnectionWizard creates a new connection wizard.
func NewConnectionWizard(opts ...WizardOption) ConnectionWizard {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))

	w := ConnectionWizard{
		step:    stepSelectProvider,
		spinner: s,
		width:   80,
		height:  24,
		styles:  defaultWizardStyles(),
		keys:    defaultWizardKeys(),
		tester:  pgxTester{},
	}
	for _, opt := range opts {
		opt(&w)
	}
	return w
}

// Init implements tea.Model.
func (w ConnectionWizard) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (w ConnectionWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

		// Step-specific handling
		switch w.step {
		case stepSelectProvider:
			return w.updateProviderSelection(msg)
		case stepSelectAuth:
			return w.updateAuthSelection(msg)
		case stepInputHost, stepInputAzure, stepInputAWS, stepInputGoogle, stepInputConnString:
			return w.updateInputForm(msg)
		case stepTestConnection:
			return w.updateTestConnection(msg)
		}

	case testResultMsg:
		w.testing = false
		w.testDone = true
		w.testOK = msg.success
		w.testErr = msg.err
		w.testInfo = msg.info
		return w, nil

	case spinner.TickMsg:
		if w.testing {
			var cmd tea.Cmd
			w.spinner, cmd = w.spinner.Update(msg)
			return w, cmd
		}

	default:
		// Forward non-key messages (focus, blink cursor) to active text input
		switch w.step {
		case stepInputHost, stepInputAzure, stepInputAWS, stepInputGoogle, stepInputConnString:
			if w.focusIndex >= 0 && w.focusIndex < len(w.inputs) {
				var cmd tea.Cmd
				w.inputs[w.focusIndex], cmd = w.inputs[w.focusIndex].Update(msg)
				return w, cmd
			}
		}
	}

	return w, nil
}

func (w ConnectionWizard) updateProviderSelection(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Up):
		if w.providerIdx > 0 {
			w.providerIdx--
		}
	case key.Matches(msg, w.keys.Down):
		if w.providerIdx < len(providers)-1 {
			w.providerIdx++
		}
	case key.Matches(msg, w.keys.Select):
		w.provider = &providers[w.providerIdx]
		if len(w.provider.AuthMethods) == 1 {
			// Skip auth selection if only one option
			w.authMethod = &w.provider.AuthMethods[0]
			w.step = w.getInputStep()
			return w, w.initInputs()
		}
		w.step = stepSelectAuth
		w.authIdx = 0
	case key.Matches(msg, w.keys.Back):
		w.result.Cancelled = true
		return w, tea.Quit
	}
	return w, nil
}

func (w ConnectionWizard) updateAuthSelection(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Up):
		if w.authIdx > 0 {
			w.authIdx--
		}
	case key.Matches(msg, w.keys.Down):
		if w.authIdx < len(w.provider.AuthMethods)-1 {
			w.authIdx++
		}
	case key.Matches(msg, w.keys.Select):
		w.authMethod = &w.provider.AuthMethods[w.authIdx]
		w.step = w.getInputStep()
		return w, w.initInputs()
	case key.Matches(msg, w.keys.Back):
		w.step = stepSelectProvider
	}
	return w, nil
}

func (w *ConnectionWizard) getInputStep() wizardStep {
	switch w.provider.ID {
	case providerAzure:
		if w.authMethod.ID == authEntra {
			return stepInputAzure
		}
		return stepInputHost
	case providerAWS:
		if w.authMethod.ID == authIAM {
			return stepInputAWS
		}
		return stepInputHost
	case providerGoogle:
		if w.authMethod.ID == authIAM {
			return stepInputGoogle
		}
		return stepInputHost
	case providerCustom:
		return stepInputConnString
	default:
		return stepInputHost
	}
}

func (w *ConnectionWizard) initInputs() tea.Cmd {
	w.inputs = nil
	w.focusIndex = 0

	switch w.step {
	case stepInputHost:
		w.inputs = w.createHostInputs()
	case stepInputAzure:
		w.inputs = w.createAzureInputs()
	case stepInputAWS:
		w.inputs = w.createAWSInputs()
	case stepInputGoogle:
		w.inputs = w.createGoogleInputs()
	case stepInputConnString:
		w.inputs = w.createConnStringInputs()
	}

	if len(w.inputs) > 0 {
		return w.inputs[0].Focus()
	}
	return nil
}

func (w *ConnectionWizard) createHostInputs() []textinput.Model {
	host := textinput.New()
	host.CharLimit = 256
	host.Width = 40
	if w.provider != nil && w.provider.ID == providerLocal {
		host.SetValue("localhost")
	} else {
		host.Placeholder = "hostname"
	}

	port := textinput.New()
	port.SetValue("5432")
	port.CharLimit = 5
	port.Width = 10

	database := textinput.New()
	database.Placeholder = "mydb"
	database.CharLimit = 64
	database.Width = 40

	mgmtDB := textinput.New()
	mgmtDB.SetValue(pgmi.DefaultManagementDB)
	mgmtDB.CharLimit = 64
	mgmtDB.Width = 40

	username := textinput.New()
	username.SetValue("postgres")
	username.CharLimit = 64
	username.Width = 40

	password := textinput.New()
	password.Placeholder = "Enter password"
	password.EchoMode = textinput.EchoPassword
	password.EchoCharacter = '•'
	password.CharLimit = 256
	password.Width = 40

	return []textinput.Model{host, port, database, mgmtDB, username, password}
}

func (w *ConnectionWizard) createAzureInputs() []textinput.Model {
	server := textinput.New()
	server.Placeholder = "myserver.postgres.database.azure.com"
	server.CharLimit = 256
	server.Width = 50

	database := textinput.New()
	database.Placeholder = "mydb"
	database.CharLimit = 64
	database.Width = 40

	username := textinput.New()
	username.Placeholder = "user@myserver"
	username.CharLimit = 128
	username.Width = 40

	return []textinput.Model{server, database, username}
}

func (w *ConnectionWizard) createAWSInputs() []textinput.Model {
	host := textinput.New()
	host.Placeholder = "mydb.xxx.us-east-1.rds.amazonaws.com"
	host.CharLimit = 256
	host.Width = 50

	port := textinput.New()
	port.SetValue("5432")
	port.CharLimit = 5
	port.Width = 10

	database := textinput.New()
	database.Placeholder = "mydb"
	database.CharLimit = 64
	database.Width = 40

	username := textinput.New()
	username.Placeholder = "iam_user"
	username.CharLimit = 64
	username.Width = 40

	region := textinput.New()
	region.Placeholder = "us-east-1"
	region.CharLimit = 32
	region.Width = 20

	return []textinput.Model{host, port, database, username, region}
}

func (w *ConnectionWizard) createGoogleInputs() []textinput.Model {
	instance := textinput.New()
	instance.Placeholder = "project:region:instance"
	instance.CharLimit = 256
	instance.Width = 50

	database := textinput.New()
	database.Placeholder = "mydb"
	database.CharLimit = 64
	database.Width = 40

	username := textinput.New()
	username.Placeholder = "iam_user@project.iam"
	username.CharLimit = 128
	username.Width = 50

	return []textinput.Model{instance, database, username}
}

func (w *ConnectionWizard) createConnStringInputs() []textinput.Model {
	connStr := textinput.New()
	connStr.Placeholder = "postgresql://user:password@host:5432/database"
	connStr.CharLimit = 512
	connStr.Width = 60

	return []textinput.Model{connStr}
}

func (w ConnectionWizard) updateInputForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, w.keys.Tab), msg.String() == "down":
		if w.focusIndex < len(w.inputs)-1 {
			w.inputs[w.focusIndex].Blur()
			w.focusIndex++
			return w, w.inputs[w.focusIndex].Focus()
		}
	case msg.String() == "shift+tab", msg.String() == "up":
		if w.focusIndex > 0 {
			w.inputs[w.focusIndex].Blur()
			w.focusIndex--
			return w, w.inputs[w.focusIndex].Focus()
		}
	case key.Matches(msg, w.keys.Select):
		// Enter on non-last field advances to next field
		if w.focusIndex < len(w.inputs)-1 {
			w.inputs[w.focusIndex].Blur()
			w.focusIndex++
			return w, w.inputs[w.focusIndex].Focus()
		}
		// Enter on last field submits the form
		if err := w.validateInputs(); err != nil {
			w.validationErr = err.Error()
			return w, nil
		}
		w.validationErr = ""
		w.buildConfig()
		w.step = stepTestConnection
		w.testing = true
		w.testDone = false
		return w, tea.Batch(w.spinner.Tick, w.testConnection())
	case key.Matches(msg, w.keys.Back):
		if w.provider != nil && len(w.provider.AuthMethods) > 1 {
			w.step = stepSelectAuth
		} else {
			w.step = stepSelectProvider
		}
		return w, nil
	default:
		w.validationErr = ""
		var cmd tea.Cmd
		w.inputs[w.focusIndex], cmd = w.inputs[w.focusIndex].Update(msg)
		return w, cmd
	}
	return w, nil
}

func (w *ConnectionWizard) validateInputs() error {
	// Basic validation - check required fields
	switch w.step {
	case stepInputHost:
		if w.inputs[2].Value() == "" { // database
			return fmt.Errorf("database name is required")
		}
	case stepInputAzure:
		if w.inputs[0].Value() == "" {
			return fmt.Errorf("server name is required")
		}
		if w.inputs[1].Value() == "" {
			return fmt.Errorf("database name is required")
		}
	case stepInputAWS:
		if w.inputs[0].Value() == "" {
			return fmt.Errorf("host is required")
		}
		if w.inputs[2].Value() == "" {
			return fmt.Errorf("database name is required")
		}
	case stepInputGoogle:
		if w.inputs[0].Value() == "" {
			return fmt.Errorf("instance connection name is required")
		}
		if w.inputs[1].Value() == "" {
			return fmt.Errorf("database name is required")
		}
	case stepInputConnString:
		if w.inputs[0].Value() == "" {
			return fmt.Errorf("connection string is required")
		}
	}
	return nil
}

func (w *ConnectionWizard) buildConfig() {
	cfg := pgmi.ConnectionConfig{
		AuthMethod:       w.authMethod.AuthMethod,
		AdditionalParams: make(map[string]string),
	}

	switch w.step {
	case stepInputHost:
		cfg.Host = w.inputs[0].Value()
		if cfg.Host == "" {
			cfg.Host = "localhost"
		}
		if port, err := strconv.Atoi(w.inputs[1].Value()); err == nil && port > 0 {
			cfg.Port = port
		} else {
			cfg.Port = 5432
		}
		cfg.Database = w.inputs[2].Value()
		w.result.ManagementDatabase = w.inputs[3].Value()
		if w.result.ManagementDatabase == "" {
			w.result.ManagementDatabase = pgmi.DefaultManagementDB
		}
		cfg.Username = w.inputs[4].Value()
		if cfg.Username == "" {
			cfg.Username = "postgres"
		}
		cfg.Password = w.inputs[5].Value()
		cfg.SSLMode = "prefer"

	case stepInputAzure:
		cfg.Host = w.inputs[0].Value()
		cfg.Port = 5432
		cfg.Database = w.inputs[1].Value()
		w.result.ManagementDatabase = pgmi.DefaultManagementDB
		cfg.Username = w.inputs[2].Value()
		cfg.SSLMode = "require"
		cfg.AuthMethod = pgmi.AuthMethodAzureEntraID

	case stepInputAWS:
		cfg.Host = w.inputs[0].Value()
		if port, err := strconv.Atoi(w.inputs[1].Value()); err == nil && port > 0 {
			cfg.Port = port
		} else {
			cfg.Port = 5432
		}
		cfg.Database = w.inputs[2].Value()
		w.result.ManagementDatabase = pgmi.DefaultManagementDB
		cfg.Username = w.inputs[3].Value()
		cfg.AWSRegion = w.inputs[4].Value()
		cfg.SSLMode = "require"
		cfg.AuthMethod = pgmi.AuthMethodAWSIAM

	case stepInputGoogle:
		cfg.GoogleInstance = w.inputs[0].Value()
		cfg.Database = w.inputs[1].Value()
		w.result.ManagementDatabase = pgmi.DefaultManagementDB
		cfg.Username = w.inputs[2].Value()
		cfg.AuthMethod = pgmi.AuthMethodGoogleIAM

	case stepInputConnString:
		// Parse connection string - for now just store it
		// The actual parsing happens in db.ParseConnectionString
		connStr := w.inputs[0].Value()
		// Simple extraction for display purposes
		cfg.Host = "from connection string"
		cfg.Database = "from connection string"
		cfg.AdditionalParams["connection_string"] = connStr
	}

	w.result.Config = cfg
}

type testResultMsg struct {
	success bool
	err     error
	info    string
}

func (w *ConnectionWizard) testConnection() tea.Cmd {
	cfg := w.result.Config
	// Test against the management database to verify server connectivity.
	// The target database may not exist yet — pgmi creates it during deployment.
	testCfg := cfg
	testCfg.Database = w.result.ManagementDatabase
	tester := w.tester
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		info, err := tester.TestConnection(ctx, testCfg)
		if err != nil {
			return testResultMsg{success: false, err: err}
		}
		return testResultMsg{success: true, info: info}
	}
}

func (w ConnectionWizard) updateTestConnection(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !w.testDone {
		return w, nil // Still testing
	}

	switch {
	case key.Matches(msg, w.keys.Select):
		if w.testOK {
			w.result.Tested = true
			w.step = stepDone
			return w, tea.Quit
		}
		// Test failed — go back to edit
		w.step = w.getInputStep()
		return w, w.initInputs()
	case key.Matches(msg, w.keys.Back):
		w.step = w.getInputStep()
		return w, w.initInputs()
	}
	return w, nil
}


// View implements tea.Model.
func (w ConnectionWizard) View() string {
	var b strings.Builder

	// Header
	b.WriteString(w.styles.Title.Render("pgmi - Connection Setup"))
	b.WriteString("\n")

	switch w.step {
	case stepSelectProvider:
		b.WriteString(w.viewProviderSelection())
	case stepSelectAuth:
		b.WriteString(w.viewAuthSelection())
	case stepInputHost:
		b.WriteString(w.viewHostForm())
	case stepInputAzure:
		b.WriteString(w.viewAzureForm())
	case stepInputAWS:
		b.WriteString(w.viewAWSForm())
	case stepInputGoogle:
		b.WriteString(w.viewGoogleForm())
	case stepInputConnString:
		b.WriteString(w.viewConnStringForm())
	case stepTestConnection:
		b.WriteString(w.viewTestConnection())
	}

	return b.String()
}

func (w ConnectionWizard) viewProviderSelection() string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render("Where is your PostgreSQL server?"))
	b.WriteString("\n\n")

	for i, p := range providers {
		cursor := "  "
		style := w.styles.Unselected
		symbol := "○"

		if i == w.providerIdx {
			cursor = ""
			style = w.styles.Selected
			symbol = "●"
		}

		b.WriteString(cursor)
		b.WriteString(style.Render(symbol + " " + p.Name))
		b.WriteString("\n")
		b.WriteString(w.styles.Description.Render(p.Description))
		b.WriteString("\n")
	}

	b.WriteString(w.styles.Help.Render("\n↑/↓ navigate • enter select • q quit"))

	return b.String()
}

func (w ConnectionWizard) viewAuthSelection() string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render(fmt.Sprintf("%s - Authentication", w.provider.Name)))
	b.WriteString("\n\n")

	for i, a := range w.provider.AuthMethods {
		cursor := "  "
		style := w.styles.Unselected
		symbol := "○"

		if i == w.authIdx {
			cursor = ""
			style = w.styles.Selected
			symbol = "●"
		}

		b.WriteString(cursor)
		b.WriteString(style.Render(symbol + " " + a.Name))
		b.WriteString("\n")
		b.WriteString(w.styles.Description.Render(a.Description))
		b.WriteString("\n")
	}

	b.WriteString(w.styles.Help.Render("\n↑/↓ navigate • enter select • esc back"))

	return b.String()
}

type formConfig struct {
	subtitle    string
	labels      []string
	hints       map[int]string
	description []string
}

func (w ConnectionWizard) viewForm(fc formConfig) string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render(fc.subtitle))
	b.WriteString("\n\n")

	for i, input := range w.inputs {
		style := w.styles.Box
		if i == w.focusIndex {
			style = w.styles.FocusedBox
		}
		b.WriteString(w.styles.Label.Render(fc.labels[i]))
		b.WriteString("\n")
		b.WriteString(style.Render(input.View()))
		if hint, ok := fc.hints[i]; ok {
			b.WriteString("\n")
			b.WriteString(w.styles.Description.Render(hint))
		}
		b.WriteString("\n\n")
	}

	for _, desc := range fc.description {
		b.WriteString(w.styles.Description.Render(desc))
		b.WriteString("\n")
	}
	if len(fc.description) > 0 {
		b.WriteString("\n")
	}

	if w.validationErr != "" {
		b.WriteString(w.styles.Error.Render("Error: " + w.validationErr))
		b.WriteString("\n\n")
	}

	b.WriteString(w.styles.Help.Render("tab/↓ next • shift+tab/↑ prev • enter submit • esc back"))

	return b.String()
}

func (w ConnectionWizard) viewHostForm() string {
	return w.viewForm(formConfig{
		subtitle: "Connection Details",
		labels:   []string{"Host:", "Port:", "Database:", "Management DB:", "Username:", "Password:"},
		hints: map[int]string{
			2: "target database — created automatically if it doesn't exist",
			3: "existing database pgmi connects to for server-level operations",
		},
	})
}

func (w ConnectionWizard) viewAzureForm() string {
	return w.viewForm(formConfig{
		subtitle:    "Azure PostgreSQL - Entra ID",
		labels:      []string{"Server:", "Database:", "Username:"},
		description: []string{"Authentication uses Azure CLI (az login) or environment variables."},
	})
}

func (w ConnectionWizard) viewAWSForm() string {
	return w.viewForm(formConfig{
		subtitle:    "AWS RDS - IAM Authentication",
		labels:      []string{"Host:", "Port:", "Database:", "Username:", "Region:"},
		description: []string{"Authentication uses AWS credentials (env vars, config file, or IAM role)."},
	})
}

func (w ConnectionWizard) viewGoogleForm() string {
	return w.viewForm(formConfig{
		subtitle: "Google Cloud SQL - IAM",
		labels:   []string{"Instance:", "Database:", "Username:"},
		description: []string{
			"Instance format: project:region:instance",
			"Authentication uses gcloud or service account.",
		},
	})
}

func (w ConnectionWizard) viewConnStringForm() string {
	var b strings.Builder

	b.WriteString(w.styles.Subtitle.Render("Connection String"))
	b.WriteString("\n\n")

	b.WriteString(w.styles.Label.Render("PostgreSQL URI:"))
	b.WriteString("\n")
	b.WriteString(w.styles.FocusedBox.Render(w.inputs[0].View()))
	b.WriteString("\n\n")

	b.WriteString(w.styles.Description.Render("Format: postgresql://user:password@host:port/database"))
	b.WriteString("\n\n")

	if w.validationErr != "" {
		b.WriteString(w.styles.Error.Render("Error: " + w.validationErr))
		b.WriteString("\n\n")
	}

	b.WriteString(w.styles.Help.Render("enter submit • esc back"))

	return b.String()
}

func (w ConnectionWizard) viewTestConnection() string {
	var b strings.Builder

	cfg := w.result.Config
	target := fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
	if cfg.Host == "" && cfg.GoogleInstance != "" {
		target = cfg.GoogleInstance + "/" + cfg.Database
	}

	b.WriteString(w.styles.Subtitle.Render("Testing Connection"))
	b.WriteString("\n\n")

	b.WriteString("Target: ")
	b.WriteString(target)
	b.WriteString("\n\n")

	if w.testing {
		b.WriteString(w.spinner.View())
		b.WriteString(" Connecting...")
	} else if w.testDone {
		if w.testOK {
			b.WriteString(w.styles.Success.Render("✓ Connected successfully"))
			b.WriteString("\n")
			b.WriteString(w.styles.Description.Render(w.testInfo))
			b.WriteString("\n\n")
			b.WriteString(w.styles.Help.Render("enter continue • esc go back"))
		} else {
			b.WriteString(w.styles.Error.Render("✗ Connection failed"))
			b.WriteString("\n")
			errMsg := "unknown error"
			if w.testErr != nil {
				errMsg = w.testErr.Error()
			}
			b.WriteString(w.styles.Description.Render(errMsg))
			b.WriteString("\n\n")
			b.WriteString(w.styles.Help.Render("enter try again • esc go back"))
		}
	}

	return b.String()
}

// Result returns the wizard result.
func (w ConnectionWizard) Result() ConnectionResult {
	return w.result
}

// Run executes the connection wizard and returns the result.
func RunConnectionWizard(opts ...WizardOption) (ConnectionResult, error) {
	wizard := NewConnectionWizard(opts...)
	p := tea.NewProgram(wizard, tea.WithAltScreen())

	model, err := p.Run()
	if err != nil {
		return ConnectionResult{Cancelled: true}, err
	}

	return model.(ConnectionWizard).Result(), nil
}
