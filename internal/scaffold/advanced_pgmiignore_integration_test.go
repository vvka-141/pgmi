package scaffold_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// .pgmiignore decides what enters the session, so a pattern that hides a
// run-once file that already ran changes the project. The run-once drift check
// must refuse the next deploy and name the file, never let it pass silently.
func TestAdvancedTemplate_PgmiignoreHidingARunOnceFileIsRefused(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := scaffoldAdvanced(t, nil)
	const hidden = "lib/core/foundation-structure.sql"
	if _, err := os.Stat(filepath.Join(projectPath, filepath.FromSlash(hidden))); err != nil {
		t.Fatalf("%s must exist in the scaffold for this test: %v", hidden, err)
	}

	const db = "pgmi_advanced_pgmiignore"
	defer testhelpers.CleanupTestDB(t, connString, db)
	params := map[string]string{"env": "dev"}
	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	ignoreFile := filepath.Join(projectPath, pgmi.PgmiignoreFile)
	f, err := os.OpenFile(ignoreFile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(hidden + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	err = deployAdvanced(t, connString, db, projectPath, false, params)
	if err == nil {
		t.Fatal("redeploy succeeded although .pgmiignore hid a run-once file that already ran")
	}
	if !strings.Contains(err.Error(), "./"+hidden) || !strings.Contains(err.Error(), "removed from the project") {
		t.Fatalf("refusal must name ./%s as removed, got: %v", hidden, err)
	}
}
