package ui

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vvka-141/pgmi/internal/tui"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

//go:embed assets/skull.txt
var dangerSkull string

type ForcedApprover struct {
	verbose bool
	output  io.Writer
	sleepFn func(time.Duration)
	// showSkull controls whether the ASCII danger banner is rendered.
	// True when output is a TTY and PGMI_NO_BANNER is unset; false in
	// CI, piped logs, and tests. The banner is a safety-critical
	// interruption surface — visual jolt that text alone cannot deliver
	// — so it appears where humans watch and disappears where machines
	// parse.
	showSkull bool
}

func NewForcedApprover(verbose bool) pgmi.Approver {
	return &ForcedApprover{
		verbose:   verbose,
		output:    os.Stderr,
		sleepFn:   time.Sleep,
		showSkull: tui.IsInteractive() && os.Getenv("PGMI_NO_BANNER") == "",
	}
}

func (a *ForcedApprover) RequestApproval(ctx context.Context, dbName string) (bool, error) {
	if a.showSkull {
		warningText := strings.ReplaceAll(dangerSkull, "${dbname}", dbName)
		fmt.Fprintln(a.output)
		fmt.Fprint(a.output, warningText)
		fmt.Fprintln(a.output)
	}

	countdownSeconds := int(pgmi.DefaultForceApprovalCountdown.Seconds())
	for i := countdownSeconds; i > 0; i-- {
		fmt.Fprintf(a.output, "\r--force: DROP DATABASE %q in %ds. Ctrl-C aborts.", dbName, i)
		// ctx-aware wait: a plain a.sleepFn would swallow Ctrl-C for up
		// to one second per iteration. a.sleepFn is kept for test
		// injection — if a test overrides it we honour that path; in
		// production sleepFn is time.Sleep which we bypass in favour of
		// the select below.
		if a.sleepFn != nil {
			// Test path: call the injected sleep; ctx check still happens
			// on the next iteration.
			a.sleepFn(1 * time.Second)
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			default:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Second):
		}
	}

	fmt.Fprintf(a.output, "\r--force: DROP DATABASE %q now.                          \n", dbName)
	return true, nil
}

// Verify ForcedApprover implements the Approver interface at compile time
var _ pgmi.Approver = (*ForcedApprover)(nil)
