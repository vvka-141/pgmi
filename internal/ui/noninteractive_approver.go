package ui

import (
	"context"
	"fmt"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// NonInteractiveApprover refuses instead of prompting.
//
// The interactive approver blocks on ReadString. Against /dev/null that is an
// immediate EOF and a clean exit 12, which is why this went unnoticed — but a CI
// runner's stdin is an open pipe that simply never answers, so the prompt hung
// for the whole of --timeout and the run then reported exit 16, "operation
// exceeded --timeout". That sends an operator hunting a slow database for what
// is a missing flag.
type NonInteractiveApprover struct{}

func NewNonInteractiveApprover() pgmi.Approver {
	return &NonInteractiveApprover{}
}

func (a *NonInteractiveApprover) RequestApproval(_ context.Context, dbName string) (bool, error) {
	return false, fmt.Errorf(
		"%w: --overwrite must be confirmed at a terminal, and this is not one; "+
			"pass --force to drop and recreate %q from a script or CI job",
		pgmi.ErrApprovalDenied, dbName)
}

var _ pgmi.Approver = (*NonInteractiveApprover)(nil)
