package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/vvka-141/pgmi/internal/tui"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// pgpassPath returns the platform-appropriate .pgpass file path.
func pgpassPath() string {
	if custom := os.Getenv("PGPASSFILE"); custom != "" {
		return custom
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "postgresql", "pgpass.conf")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pgpass")
}

// offerSavePgpass prompts the user to save the password to .pgpass after a successful wizard.
// Does nothing if password is empty, terminal is non-interactive, or user declines.
func offerSavePgpass(cfg *pgmi.ConnectionConfig) {
	if cfg.Password == "" || !tui.IsInteractive() {
		return
	}

	fmt.Fprintln(os.Stderr, "")
	if !tui.PromptContinue("Save password to .pgpass for future sessions?") {
		fmt.Fprintln(os.Stderr, "Tip: provide password via $PGPASSWORD, .pgpass, or connection string.")
		return
	}

	if err := writePgpassEntry(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save .pgpass: %v\n", err)
		fmt.Fprintln(os.Stderr, "Tip: provide password via $PGPASSWORD or connection string.")
		return
	}

	path := pgpassPath()
	fmt.Fprintf(os.Stderr, "Saved to %s\n", path)
}

// writePgpassEntry adds or updates a .pgpass entry for the given connection.
func writePgpassEntry(cfg *pgmi.ConnectionConfig) error {
	path := pgpassPath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory")
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	host := escapePgpass(cfg.Host)
	port := fmt.Sprintf("%d", cfg.Port)
	db := escapePgpass(cfg.Database)
	user := escapePgpass(cfg.Username)
	password := escapePgpass(cfg.Password)

	newEntry := fmt.Sprintf("%s:%s:%s:%s:%s", host, port, db, user, password)
	matchPrefix := fmt.Sprintf("%s:%s:%s:%s:", host, port, db, user)

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read existing .pgpass: %w", err)
	}

	// Replace existing entry or append
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, matchPrefix) {
			lines[i] = newEntry
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, newEntry)
	}

	content := strings.Join(lines, "\n") + "\n"

	// Write with restricted permissions (0600 required by PostgreSQL on Unix)
	return os.WriteFile(path, []byte(content), 0600)
}

// escapePgpass escapes colons and backslashes in a .pgpass field value.
func escapePgpass(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `:`, `\:`)
	return s
}
