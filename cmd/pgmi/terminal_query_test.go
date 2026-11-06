//go:build linux

package main

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestNoTerminalQueryOutsideWizards runs pgmi on a pty that never answers, as
// agents drive it. A command that queries the terminal (OSC 11, DSR) waits for
// an answer that never comes.
func TestNoTerminalQueryOutsideWizards(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the CLI on a pty")
	}

	cases := map[string][]string{
		"version": {"version"},
		"help":    {"--help"},
		"deploy":  {"deploy", t.TempDir(), "--connection", "postgresql://u@127.0.0.1:1/db", "--timeout", "1s"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := pgmiCommand(t, args...)
			ptmx, err := pty.Start(cmd)
			if err != nil {
				t.Fatalf("starting on a pty: %v", err)
			}
			defer ptmx.Close()

			start := time.Now()
			var out bytes.Buffer
			copied := make(chan struct{})
			go func() {
				_, _ = io.Copy(&out, ptmx)
				close(copied)
			}()
			_ = cmd.Wait()
			elapsed := time.Since(start)
			_ = ptmx.Close()
			<-copied

			if elapsed > time.Second {
				t.Errorf("pgmi %v took %v on a silent pty, want under 1s", args, elapsed)
			}
			if bytes.Contains(out.Bytes(), []byte("\x1b]11;?")) {
				t.Errorf("pgmi %v queried the terminal background colour:\n%q", args, out.String())
			}
		})
	}
}
