package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
)

// `pgmi deploy --json | jq` only works if stdout carries the envelope and
// nothing else. internal/cli covers the envelope's shape, but by calling
// printDeployJSON directly — so the parts most likely to contaminate stdout are
// never in the frame: session-preparation progress, the RAISE NOTICE stream
// coming back from PostgreSQL, the deploy summary line, and the DONE banner the
// scaffolded deploy.sql prints. Any one of them written to stdout instead of
// stderr breaks every machine consumer, and every existing test still passes.
//
// Running the real CLI as a subprocess is the only way to see that. The child
// is this test binary re-execed via TestMain, so no toolchain runs here.
func TestDeployJSON_StdoutCarriesTheEnvelopeAndNothingElse(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	// --verbose swaps the notice handler: deploy.go installs a timing-prefixed
	// one, so the default handler in internal/db is only reachable without it.
	// Both stream to stderr and both have to keep doing so, so the cases below
	// split on the flag deliberately — an earlier draft passed --verbose
	// everywhere and left the default handler unexercised.
	for _, tc := range []struct {
		name      string
		deploySQL string
		verbose   bool
		wantOK    bool
	}{
		{
			// Notices are the interesting part: they arrive from the server
			// mid-deploy, on the same connection, and have to be routed to
			// stderr by the notice handler.
			name: "success with notices, default handler",
			deploySQL: "DO $$ BEGIN\n" +
				"  RAISE NOTICE 'a notice that must not reach stdout';\n" +
				"  RAISE WARNING 'a warning that must not reach stdout';\n" +
				"END $$;\nSELECT 1;\n",
			wantOK: true,
		},
		{
			name: "success with notices, verbose timing handler",
			deploySQL: "DO $$ BEGIN\n" +
				"  RAISE NOTICE 'a notice that must not reach stdout';\n" +
				"END $$;\nSELECT 1;\n",
			verbose: true,
			wantOK:  true,
		},
		{
			// The failure path formats an error and prints a summary; both are
			// human output and belong on stderr.
			name:      "failure",
			deploySQL: "BEGIN;\nSELECT 1/0;\nCOMMIT;\n",
			verbose:   true,
			wantOK:    false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, "deploy.sql"),
				[]byte(tc.deploySQL), 0o644); err != nil {
				t.Fatalf("write deploy.sql: %v", err)
			}

			args := []string{"deploy", projectDir,
				"--connection", connString,
				"--database", "pgmi_json_purity",
				"--overwrite", "--force", "--json"}
			if tc.verbose {
				args = append(args, "--verbose")
			}
			cmd := pgmiCommand(t, args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			_ = cmd.Run() // a failing deploy exits non-zero by design

			var envelope map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("stdout is not a single JSON value, so `--json | jq` is broken: %v\n"+
					"stdout was:\n%s", err, stdout.String())
			}
			if _, ok := envelope["status"]; !ok {
				t.Errorf("envelope has no status field: %v", envelope)
			}
			if tc.wantOK != (envelope["status"] == "success") {
				t.Errorf("status = %v, wantOK = %v", envelope["status"], tc.wantOK)
			}

			// Guard the guard. Purity is trivial to achieve by emitting nothing
			// at all, so require that the human output still happened — and on
			// the other stream.
			if strings.TrimSpace(stderr.String()) == "" {
				t.Error("stderr is empty: stdout is pure only because nothing was reported")
			}
			if tc.wantOK && !strings.Contains(stderr.String(), "must not reach stdout") {
				t.Errorf("the NOTICE/WARNING never reached stderr either, so this run did not "+
					"exercise the notice path:\n%s", stderr.String())
			}
		})
	}
}
