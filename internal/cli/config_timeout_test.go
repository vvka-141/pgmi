package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// resolveEffectiveTimeout had no test at all, and its one error path exited 1
// while every other fault in pgmi.yaml exits 10 — an unknown field, a port that
// is not a number, a connection block that is a scalar. A bad duration in that
// file is the same kind of mistake and deserves the same code, which is what a
// CI script reads to tell "your config is wrong" from "something broke".
func TestResolveEffectiveTimeout(t *testing.T) {
	newCmd := func(timeoutChanged bool) *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().Duration("timeout", 3*time.Minute, "")
		if timeoutChanged {
			// Marks the flag Changed, which is how the real command signals
			// that --timeout was given explicitly.
			if err := cmd.Flags().Set("timeout", "45s"); err != nil {
				t.Fatalf("set timeout flag: %v", err)
			}
		}
		return cmd
	}

	t.Run("bad duration in pgmi.yaml is invalid configuration", func(t *testing.T) {
		for _, bad := range []string{"42", "5 fortnights", "later"} {
			cfg := &config.ProjectConfig{Timeout: bad}
			_, err := resolveEffectiveTimeout(newCmd(false), cfg, time.Minute)
			if err == nil {
				t.Fatalf("timeout %q was accepted", bad)
			}
			if !errors.Is(err, pgmi.ErrInvalidConfig) {
				t.Errorf("timeout %q: not an ErrInvalidConfig chain, so this exits 1: %v", bad, err)
			}
			if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
				t.Errorf("timeout %q: exit code %d, want %d", bad, got, pgmi.ExitConfigError)
			}
			// The original parse failure has to survive, or the message stops
			// saying which value was wrong.
			if !strings.Contains(err.Error(), bad) {
				t.Errorf("timeout %q: the offending value is missing from %v", bad, err)
			}
		}
	})

	t.Run("valid duration in pgmi.yaml is used", func(t *testing.T) {
		got, err := resolveEffectiveTimeout(newCmd(false), &config.ProjectConfig{Timeout: "90s"}, time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 90*time.Second {
			t.Errorf("timeout = %v, want 90s", got)
		}
	})

	t.Run("an explicit --timeout outranks pgmi.yaml, even a bad one", func(t *testing.T) {
		got, err := resolveEffectiveTimeout(newCmd(true), &config.ProjectConfig{Timeout: "nonsense"}, 45*time.Second)
		if err != nil {
			t.Fatalf("the flag was set, so pgmi.yaml should not be parsed at all: %v", err)
		}
		if got != 45*time.Second {
			t.Errorf("timeout = %v, want the flag's 45s", got)
		}
	})

	t.Run("no pgmi.yaml falls back to the flag", func(t *testing.T) {
		got, err := resolveEffectiveTimeout(newCmd(false), nil, 2*time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 2*time.Minute {
			t.Errorf("timeout = %v, want 2m", got)
		}
	})
}

// A .env that exists but does not parse loads NOTHING — godotenv is
// all-or-nothing — so the PGPASSWORD and PGDATABASE in it are simply absent and
// the deploy fails later with "database name is required". That misdiagnosis
// used to be the only thing the user saw: the real cause printed under
// --verbose, which is exactly when nobody is looking.
//
// The warning must not quote the file. godotenv's error carries the text around
// the failure ("... near \"<content>\""), and this is the file credentials live
// in; printing it would put PGPASSWORD on stderr and into CI logs.
func TestLoadProjectConfig_UnparseableEnvWarnsWithoutQuotingTheFile(t *testing.T) {
	const secret = "SUPERSECRET123"

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("this is not kv\nPGPASSWORD="+secret+"\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	stderr := captureStderr(t, func() {
		if _, err := loadProjectConfig(dir); err != nil {
			t.Errorf("an unparseable .env must warn, not fail the load: %v", err)
		}
	})

	if !strings.Contains(stderr, "WARNING") {
		t.Errorf("no warning for an unparseable .env; the deploy fails later with an "+
			"unrelated complaint:\n%s", stderr)
	}
	if !strings.Contains(stderr, "none of its values were applied") {
		t.Errorf("the warning does not say the file was discarded whole:\n%s", stderr)
	}
	if strings.Contains(stderr, secret) {
		t.Errorf("the warning quoted the .env and leaked a credential:\n%s", stderr)
	}
}

// A clean .env, and a missing one, must both stay silent.
func TestLoadProjectConfig_ValidOrAbsentEnvIsSilent(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"valid .env", "PGHOST=localhost\n# a comment\n\nPGPORT=5432\n"},
		{"no .env", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.content != "" {
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tc.content), 0o600); err != nil {
					t.Fatalf("write .env: %v", err)
				}
			}
			stderr := captureStderr(t, func() {
				if _, err := loadProjectConfig(dir); err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			})
			if strings.TrimSpace(stderr) != "" {
				t.Errorf("expected silence, got:\n%s", stderr)
			}
		})
	}
}
