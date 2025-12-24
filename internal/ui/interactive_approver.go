package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// InteractiveApprover implements the Approver interface for console-based
// interactive confirmation. It prompts the user to type the database name
// to confirm destructive operations.
type InteractiveApprover struct {
	verbose bool
}

// NewInteractiveApprover creates a new InteractiveApprover.
func NewInteractiveApprover(verbose bool) pgmi.Approver {
	return &InteractiveApprover{verbose: verbose}
}

// RequestApproval prompts the user to type the database name to confirm.
func (a *InteractiveApprover) RequestApproval(ctx context.Context, dbName string) (bool, error) {
	fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: You are about to DROP and RECREATE the database '%s'\n", dbName)
	fmt.Fprintln(os.Stderr, "This will permanently delete all data in this database!")
	fmt.Fprintf(os.Stderr, "\nTo confirm, type the database name '%s' and press Enter: ", dbName)

	// Read user input with context cancellation support
	inputChan := make(chan string, 1)
	errChan := make(chan error, 1)

	go func() {
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			errChan <- err
			return
		}
		inputChan <- strings.TrimSpace(input)
	}()

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case err := <-errChan:
		return false, fmt.Errorf("failed to read input: %w", err)
	case input := <-inputChan:
		if input == dbName {
			fmt.Fprintln(os.Stderr, "✓ Confirmed. Proceeding with database overwrite...")
			return true, nil
		}
		fmt.Fprintf(os.Stderr, "✗ Input '%s' does not match database name '%s'. Operation cancelled.\n", input, dbName)
		return false, nil
	}
}

// Verify InteractiveApprover implements the Approver interface at compile time
var _ pgmi.Approver = (*InteractiveApprover)(nil)
