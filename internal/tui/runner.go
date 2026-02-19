package tui

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/tui/wizards"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// SetupResult represents the result of a guided setup flow.
type SetupResult struct {
	Cancelled  bool
	Connection pgmi.ConnectionConfig
	Config     config.ProjectConfig
	SavedPath  string
}

// RunSetup runs the full guided setup flow: connection wizard → config wizard → save.
// Returns the setup result or an error.
func RunSetup() (SetupResult, error) {
	// Step 1: Connection wizard
	connResult, err := wizards.RunConnectionWizard()
	if err != nil {
		return SetupResult{Cancelled: true}, err
	}
	if connResult.Cancelled {
		return SetupResult{Cancelled: true}, nil
	}

	// Step 2: Check save choice
	saveChoice := 0 // Will be set by wizard

	// If user chose to save, run config wizard
	if saveChoice == 0 {
		cfgResult, err := wizards.RunConfigWizard(connResult.Config)
		if err != nil {
			return SetupResult{Cancelled: true}, err
		}
		if cfgResult.Cancelled {
			return SetupResult{Cancelled: true}, nil
		}

		// Save the config
		if err := saveConfig(".", cfgResult.Config); err != nil {
			return SetupResult{Cancelled: true}, fmt.Errorf("failed to save config: %w", err)
		}

		return SetupResult{
			Connection: connResult.Config,
			Config:     cfgResult.Config,
			SavedPath:  "pgmi.yaml",
		}, nil
	}

	// User chose session-only
	return SetupResult{
		Connection: connResult.Config,
	}, nil
}

// RunConnectionOnly runs just the connection wizard without config building.
func RunConnectionOnly() (pgmi.ConnectionConfig, error) {
	result, err := wizards.RunConnectionWizard()
	if err != nil {
		return pgmi.ConnectionConfig{}, err
	}
	if result.Cancelled {
		return pgmi.ConnectionConfig{}, fmt.Errorf("cancelled")
	}
	return result.Config, nil
}

// ShouldRunWizard determines if the TUI wizard should run.
// Returns true when:
// - Running interactively (TTY detected)
// - No pgmi.yaml exists OR connection info is incomplete
// - Force flag is not set
func ShouldRunWizard(sourcePath string, hasForceFlag bool) bool {
	if hasForceFlag {
		return false
	}
	if !IsInteractive() {
		return false
	}

	// Check if pgmi.yaml exists and is complete
	cfg, err := config.Load(sourcePath)
	if err != nil {
		// No config or error loading - should run wizard
		return true
	}

	// Check if config has minimum required connection info
	// At minimum we need either host+database or a way to connect
	if cfg.Connection.Host == "" && cfg.Connection.Database == "" {
		return true
	}

	return false
}

// saveConfig writes a ProjectConfig to pgmi.yaml in the given directory.
func saveConfig(dir string, cfg config.ProjectConfig) error {
	path := filepath.Join(dir, "pgmi.yaml")

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// PromptContinue shows a simple prompt asking if the user wants to continue.
// Returns true if user confirms, false otherwise.
func PromptContinue(message string) bool {
	if !IsInteractive() {
		return true
	}

	fmt.Printf("%s [Y/n]: ", message)

	var response string
	fmt.Scanln(&response)

	return response == "" || response == "y" || response == "Y"
}

// ShowProgress displays a progress message with optional spinner.
type ProgressDisplay struct {
	program *tea.Program
}

// NewProgressDisplay creates a progress display.
func NewProgressDisplay() *ProgressDisplay {
	return &ProgressDisplay{}
}

// Start begins showing progress with the given message.
func (p *ProgressDisplay) Start(message string) {
	if !IsInteractive() {
		fmt.Println(message)
		return
	}
	fmt.Printf("◐ %s\n", message)
}

// Success marks the progress as successful.
func (p *ProgressDisplay) Success(message string) {
	fmt.Printf("✓ %s\n", message)
}

// Error marks the progress as failed.
func (p *ProgressDisplay) Error(message string) {
	fmt.Printf("✗ %s\n", message)
}
