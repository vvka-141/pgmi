// Package deploy runs a pgmi deployment from Go, wired as the pgmi CLI wires
// it: file discovery, the session tables, deploy.sql and its execution.
//
// The API is pre-1.0 and may change between minor versions.
package deploy

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

const modulePath = "github.com/vvka-141/pgmi"

// Deployer runs deployments. It holds no connection; each Deploy opens and
// closes its own, so one Deployer serves concurrent deploys.
type Deployer struct {
	approver pgmi.Approver
	logger   pgmi.Logger
	notice   func(severity, message, detail, hint string)
	version  string
}

// Option configures a Deployer.
type Option func(*Deployer)

// WithApprover decides whether an existing database may be dropped and
// recreated when DeploymentConfig.Overwrite is set. Without it, an overwrite
// of an existing database is refused unless DeploymentConfig.Force is set.
func WithApprover(a pgmi.Approver) Option {
	return func(d *Deployer) { d.approver = a }
}

// WithLogger receives pgmi's own progress messages, connection retries and
// warnings. PostgreSQL notices go to WithNoticeHandler instead. The default,
// also kept for nil, discards everything.
func WithLogger(l pgmi.Logger) Option {
	return func(d *Deployer) {
		if l != nil {
			d.logger = l
		}
	}
}

// WithNoticeHandler receives each notice PostgreSQL sends while this
// Deployer's deploys run (RAISE NOTICE, WARNING, INFO, ...), with its
// unlocalized severity. Server DEBUG chatter is dropped; RAISE DEBUG is kept.
// Concurrent deploys call it concurrently. The default, also nil, discards
// notices.
func WithNoticeHandler(h func(severity, message, detail, hint string)) Option {
	return func(d *Deployer) { d.notice = h }
}

// WithVersion sets the pgmi version the session reports to deploy.sql. The
// default, also kept for "", is the version of this module in the build.
func WithVersion(v string) Option {
	return func(d *Deployer) {
		if v != "" {
			d.version = v
		}
	}
}

// New returns a Deployer.
func New(opts ...Option) *Deployer {
	d := &Deployer{logger: logging.NewNullLogger(), version: moduleVersion()}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Deploy runs the project at cfg.SourcePath against cfg.DatabaseName. The
// result says how far the deploy got and is non-nil whenever the deploy
// started, failed or not; pgmi.ExitCodeForError maps the error to the CLI's
// exit code.
func (d *Deployer) Deploy(ctx context.Context, cfg pgmi.DeploymentConfig) (*pgmi.DeployResult, error) {
	approver := d.approver
	if approver == nil {
		approver = forceApprover(cfg.Force)
	}
	connectors := db.ConnectorFactory(db.Output{Notice: d.notice, Logger: d.logger})
	sessions := services.NewSessionManager(connectors, scanner.NewScanner(checksum.New()), loader.NewLoader(), d.logger, d.version)
	svc := services.NewDeploymentService(connectors, approver, d.logger, sessions, manager.New())
	err := svc.Deploy(ctx, cfg)
	return svc.LastResult(), err
}

type forceApprover bool

func (f forceApprover) RequestApproval(_ context.Context, dbName string) (bool, error) {
	if f {
		return true, nil
	}
	return false, fmt.Errorf("%w: overwriting %q needs DeploymentConfig.Force or deploy.WithApprover", pgmi.ErrApprovalDenied, dbName)
}

func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	version := info.Main.Version
	if info.Main.Path != modulePath {
		version = ""
		for _, dep := range info.Deps {
			if dep.Path == modulePath {
				version = dep.Version
			}
		}
	}
	if version == "" || version == "(devel)" {
		return "dev"
	}
	return strings.TrimPrefix(version, "v")
}
