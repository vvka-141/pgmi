package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// The connection wizard asks for a password, and pgmi.yaml is a file people
// commit. Nothing may carry the one into the other.
//
// Today that holds structurally — config.ConnectionConfig has no password
// field, so yaml.Marshal cannot emit one — but structure is not a promise
// until something checks it. Adding a Password field for convenience would
// silently start writing credentials into a project file, and every existing
// test would still pass.
func TestSavedConfigNeverContainsSecrets(t *testing.T) {
	const (
		password     = "sup3r-secret-pw"
		azureSecret  = "azure-client-secret-value"
		sslKeyPasswd = "ssl-key-passphrase"
	)

	dir := t.TempDir()
	err := saveConnectionToConfig(dir, &pgmi.ConnectionConfig{
		Host:              "db.example.com",
		Port:              5432,
		Username:          "deploy",
		Database:          "myapp",
		Password:          password,
		SSLMode:           "require",
		SSLPassword:       sslKeyPasswd,
		AuthMethod:        pgmi.AuthMethodAzureEntraID,
		AzureTenantID:     "tenant-id",
		AzureClientID:     "client-id",
		AzureClientSecret: azureSecret,
	}, "postgres")
	if err != nil {
		t.Fatalf("saveConnectionToConfig: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "pgmi.yaml"))
	if err != nil {
		t.Fatalf("read pgmi.yaml: %v", err)
	}
	written := string(body)

	for _, secret := range []struct{ name, value string }{
		{"database password", password},
		{"Azure client secret", azureSecret},
		{"SSL key passphrase", sslKeyPasswd},
	} {
		if strings.Contains(written, secret.value) {
			t.Errorf("pgmi.yaml contains the %s — it is a file users commit:\n%s",
				secret.name, written)
		}
	}

	// The non-secret fields must survive, or the test would pass on an empty file.
	for _, want := range []string{"db.example.com", "deploy", "myapp", "tenant-id"} {
		if !strings.Contains(written, want) {
			t.Errorf("pgmi.yaml lost %q — the check above proves nothing if nothing is written:\n%s",
				want, written)
		}
	}

	// 0600 is the other half: even without a secret inside, this file accrues
	// them through user edits.
	//
	// Unix only. Go's mode bits do not reach NTFS ACLs, so a file written 0600
	// on Windows reports 0666 and the write mode is advisory there — the
	// .pgpass-style privacy the code aims for holds on the platforms CI and
	// production use, not on the one this is often developed on.
	if runtime.GOOS == "windows" {
		t.Log("skipping the mode check: Go permission bits do not apply on Windows")
		return
	}
	info, err := os.Stat(filepath.Join(dir, "pgmi.yaml"))
	if err != nil {
		t.Fatalf("stat pgmi.yaml: %v", err)
	}
	if perm := info.Mode().Perm(); perm&fs.FileMode(0o077) != 0 {
		t.Errorf("pgmi.yaml mode is %04o, want no group or world access", perm)
	}
}
