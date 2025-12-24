package ui

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

//go:embed assets/skull.txt
var dangerSkull string

// ForcedApprover implements the Approver interface for forced (non-interactive)
// approval. It displays a countdown and automatically approves after the countdown,
// used when the --force flag is provided.
type ForcedApprover struct {
	verbose bool
}

// NewForcedApprover creates a new ForcedApprover.
func NewForcedApprover(verbose bool) pgmi.Approver {
	return &ForcedApprover{verbose: verbose}
}

// RequestApproval displays a countdown and automatically approves after the countdown.
func (a *ForcedApprover) RequestApproval(ctx context.Context, dbName string) (bool, error) {
	// Display dramatic warning with skull - substitute database name
	warningText := strings.ReplaceAll(dangerSkull, "${dbname}", dbName)
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, warningText)
	fmt.Fprintln(os.Stderr)

	countdownSeconds := int(pgmi.DefaultForceApprovalCountdown.Seconds())
	for i := countdownSeconds; i > 0; i-- {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
			fmt.Fprintf(os.Stderr, "\rDropping in: %d seconds... (Press Ctrl+C to cancel)", i)
			time.Sleep(1 * time.Second)
		}
	}

	fmt.Fprintf(os.Stderr, "\r✓ Proceeding with database overwrite...                              \n")
	return true, nil
}

// Verify ForcedApprover implements the Approver interface at compile time
var _ pgmi.Approver = (*ForcedApprover)(nil)
