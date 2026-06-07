package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// InteractiveApprover is a one-shot, CLI-only approver. When the input is
// os.Stdin (the only production path), a ctx cancellation returns
// immediately and leaves the stdin-reading goroutine blocked on
// ReadString — it is released when the OS closes stdin at process exit.
// This is acceptable for a one-shot CLI but makes this approver UNSUITABLE
// for library callers that create many approvers within a single process.
//
// The sync.WaitGroup coordinates the non-stdin (test-injected) path where
// we can close the reader explicitly; it is deliberately not waited on
// when input == os.Stdin because closing os.Stdin in a shared process is
// a hostile act.
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
	fmt.Fprintf(a.output, "\nWARNING: about to DROP and RECREATE database %q. This deletes all data.\n", dbName)
	fmt.Fprintf(a.output, "Type the database name to confirm: ")

	inputChan := make(chan string, 1)
	errChan := make(chan error, 1)

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		reader := bufio.NewReader(a.input)
		input, err := reader.ReadString('\n')
		if err != nil {
			select {
			case errChan <- err:
			default:
			}
			return
		}
		select {
		case inputChan <- strings.TrimSpace(input):
		default:
		}
	}()

	select {
	case <-ctx.Done():
		// Non-stdin (tests): unblock the reader goroutine and wait for it
		// to exit so the caller can safely discard the approver.
		// Stdin (production): leak the goroutine — see package doc.
		if closer, ok := a.input.(io.Closer); ok && a.input != os.Stdin {
			closer.Close()
			wg.Wait()
		}
		return false, ctx.Err()
	case err := <-errChan:
		wg.Wait()
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(a.output, "\nNo input available. Use --force to bypass confirmation in non-interactive mode.")
			return false, pgmi.ErrApprovalDenied
		}
		return false, fmt.Errorf("failed to read input: %w", err)
	case input := <-inputChan:
		wg.Wait()
		if input == dbName {
			fmt.Fprintln(a.output, "Confirmed.")
			return true, nil
		}
		fmt.Fprintf(a.output, "%q does not match %q. Cancelled.\n", input, dbName)
		return false, nil
	}
}

// Verify InteractiveApprover implements the Approver interface at compile time
var _ pgmi.Approver = (*InteractiveApprover)(nil)
