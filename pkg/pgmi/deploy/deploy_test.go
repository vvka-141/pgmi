package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestTheDefaultApproverRefusesAnOverwriteWithoutForce(t *testing.T) {
	ok, err := forceApprover(false).RequestApproval(context.Background(), "app")
	if ok || !errors.Is(err, pgmi.ErrApprovalDenied) {
		t.Fatalf("without Force: approved=%v err=%v, want a refusal wrapping ErrApprovalDenied", ok, err)
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitApprovalDenied {
		t.Errorf("exit code %d, want %d", got, pgmi.ExitApprovalDenied)
	}
	if ok, err := forceApprover(true).RequestApproval(context.Background(), "app"); !ok || err != nil {
		t.Fatalf("with Force: approved=%v err=%v, want approval", ok, err)
	}
}

func TestOptionsReplaceTheDefaults(t *testing.T) {
	d := New(WithVersion("1.2.3"))
	if d.version != "1.2.3" || d.approver != nil || d.logger == nil {
		t.Fatalf("New(WithVersion) = %+v", d)
	}
	if New().version == "" {
		t.Fatal("the default version must not be empty: the session refuses one")
	}
}

func TestNilAndEmptyOptionsKeepTheDefaults(t *testing.T) {
	d := New(WithLogger(nil), WithVersion(""), WithNoticeHandler(nil))
	if d.logger == nil || d.version == "" {
		t.Fatalf("New with nil/empty options = %+v", d)
	}
	result, err := d.Deploy(context.Background(), pgmi.DeploymentConfig{
		SourcePath:          t.TempDir(),
		ConnectionString:    "postgresql://nobody@127.0.0.1:1/postgres",
		MaintenanceDatabase: "postgres",
		DatabaseName:        "app",
	})
	if !errors.Is(err, pgmi.ErrDeploySQLNotFound) {
		t.Fatalf("Deploy of a project without deploy.sql: result=%+v err=%v, want ErrDeploySQLNotFound", result, err)
	}
}
