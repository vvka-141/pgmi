package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

func TestDeploy_NoticeFormat(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	const deploySQL = "DO $$ BEGIN\n" +
		"  RAISE NOTICE 'plain notice';\n" +
		"  RAISE WARNING 'this is a warning' USING DETAIL = 'the detail', HINT = 'the hint';\n" +
		"END $$;\n"

	for _, verbose := range []bool{false, true} {
		name := "default"
		prefix := ``
		if verbose {
			name = "verbose"
			prefix = `\[\d+\.\d{2}s\] `
		}
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, "deploy.sql"), []byte(deploySQL), 0o644); err != nil {
				t.Fatalf("write deploy.sql: %v", err)
			}
			args := []string{"deploy", projectDir,
				"--connection", connString,
				"--database", "pgmi_notice_format",
				"--overwrite", "--force"}
			if verbose {
				args = append(args, "--verbose")
			}
			cmd := pgmiCommand(t, args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("deploy failed: %v\n%s", err, stderr.String())
			}

			for _, line := range []string{
				`plain notice`,
				`WARNING:  this is a warning`,
				`DETAIL:  the detail`,
				`HINT:  the hint`,
			} {
				re := regexp.MustCompile(`(?m)^` + prefix + regexp.QuoteMeta(line) + `$`)
				if !re.Match(stderr.Bytes()) {
					t.Errorf("stderr has no line %q:\n%s", re, stderr.String())
				}
			}
		})
	}
}
