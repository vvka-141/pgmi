package ui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type InteractiveApprover struct {
	verbose bool
	input   io.Reader
	output  io.Writer
}

func NewInteractiveApprover(verbose bool) pgmi.Approver {
	return &InteractiveApprover{
		verbose: verbose,
		input:   os.Stdin,
		output:  os.Stderr,
	}
}

func (a *InteractiveApprover) RequestApproval(ctx context.Context, dbName string) (bool, error) {
	fmt.Fprintf(a.output, "\n⚠️  WARNING: You are about to DROP and RECREATE the database '%s'\n", dbName)
	fmt.Fprintln(a.output, "This will permanently delete all data in this database!")
	fmt.Fprintf(a.output, "\nTo confirm, type the database name '%s' and press Enter: ", dbName)

	inputChan := make(chan string, 1)
	errChan := make(chan error, 1)

	go func() {
		reader := bufio.NewReader(a.input)
		input, err := reader.ReadString('\n')
		if err != nil {
			errChan <- err
			return
		}
		inputChan <- strings.TrimSpace(input)
	}()

	select {
	case <-ctx.Done():
		if closer, ok := a.input.(io.Closer); ok {
			closer.Close()
		}
		return false, ctx.Err()
	case err := <-errChan:
		return false, fmt.Errorf("failed to read input: %w", err)
	case input := <-inputChan:
		if input == dbName {
			fmt.Fprintln(a.output, "✓ Confirmed. Proceeding with database overwrite...")
			return true, nil
		}
		fmt.Fprintf(a.output, "✗ Input '%s' does not match database name '%s'. Operation cancelled.\n", input, dbName)
		return false, nil
	}
}

// Verify InteractiveApprover implements the Approver interface at compile time
var _ pgmi.Approver = (*InteractiveApprover)(nil)
