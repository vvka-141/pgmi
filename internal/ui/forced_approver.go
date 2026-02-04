package ui

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

//go:embed assets/skull.txt
var dangerSkull string

type ForcedApprover struct {
	verbose bool
	output  io.Writer
	sleepFn func(time.Duration)
}

func NewForcedApprover(verbose bool) pgmi.Approver {
	return &ForcedApprover{
		verbose: verbose,
		output:  os.Stderr,
		sleepFn: time.Sleep,
	}
}

func (a *ForcedApprover) RequestApproval(ctx context.Context, dbName string) (bool, error) {
	warningText := strings.ReplaceAll(dangerSkull, "${dbname}", dbName)
	fmt.Fprintln(a.output)
	fmt.Fprint(a.output, warningText)
	fmt.Fprintln(a.output)

	countdownSeconds := int(pgmi.DefaultForceApprovalCountdown.Seconds())
	for i := countdownSeconds; i > 0; i-- {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
			fmt.Fprintf(a.output, "\rDropping in: %d seconds... (Press Ctrl+C to cancel)", i)
			a.sleepFn(1 * time.Second)
		}
	}

	fmt.Fprintf(a.output, "\r✓ Proceeding with database overwrite...                              \n")
	return true, nil
}

// Verify ForcedApprover implements the Approver interface at compile time
var _ pgmi.Approver = (*ForcedApprover)(nil)
