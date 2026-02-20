package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRequireProjectPath(t *testing.T) {
	cmd := &cobra.Command{
		Use: "deploy <project_path>",
	}

	t.Run("returns error when no args", func(t *testing.T) {
		err := RequireProjectPath(cmd, []string{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "missing required argument: <project_path>") {
			t.Errorf("expected error to contain 'missing required argument: <project_path>', got: %s", err.Error())
		}
		if !strings.Contains(err.Error(), "Example:") {
			t.Errorf("expected error to contain 'Example:', got: %s", err.Error())
		}
	})

	t.Run("returns nil when arg provided", func(t *testing.T) {
		err := RequireProjectPath(cmd, []string{"./migrations"})
		if err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
	})

	t.Run("returns error when too many args", func(t *testing.T) {
		err := RequireProjectPath(cmd, []string{"a", "b"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "accepts 1 arg") {
			t.Errorf("expected error to contain 'accepts 1 arg', got: %s", err.Error())
		}
	})
}

func TestRequireTemplateName(t *testing.T) {
	cmd := &cobra.Command{
		Use: "describe <template_name>",
	}

	t.Run("returns error when no args", func(t *testing.T) {
		err := RequireTemplateName(cmd, []string{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "missing required argument: <template_name>") {
			t.Errorf("expected error to contain 'missing required argument: <template_name>', got: %s", err.Error())
		}
		if !strings.Contains(err.Error(), "pgmi templates list") {
			t.Errorf("expected error to contain 'pgmi templates list', got: %s", err.Error())
		}
	})

	t.Run("returns nil when arg provided", func(t *testing.T) {
		err := RequireTemplateName(cmd, []string{"basic"})
		if err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
	})

	t.Run("returns error when too many args", func(t *testing.T) {
		err := RequireTemplateName(cmd, []string{"a", "b"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "accepts 1 arg") {
			t.Errorf("expected error to contain 'accepts 1 arg', got: %s", err.Error())
		}
	})
}

func TestRequireSkillName(t *testing.T) {
	cmd := &cobra.Command{
		Use: "skill <name>",
	}

	t.Run("returns error when no args", func(t *testing.T) {
		err := RequireSkillName(cmd, []string{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "missing required argument: <name>") {
			t.Errorf("expected error to contain 'missing required argument: <name>', got: %s", err.Error())
		}
		if !strings.Contains(err.Error(), "pgmi ai skills") {
			t.Errorf("expected error to contain 'pgmi ai skills', got: %s", err.Error())
		}
	})

	t.Run("returns nil when arg provided", func(t *testing.T) {
		err := RequireSkillName(cmd, []string{"pgmi-sql"})
		if err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
	})

	t.Run("returns error when too many args", func(t *testing.T) {
		err := RequireSkillName(cmd, []string{"a", "b"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "accepts 1 arg") {
			t.Errorf("expected error to contain 'accepts 1 arg', got: %s", err.Error())
		}
	})
}
