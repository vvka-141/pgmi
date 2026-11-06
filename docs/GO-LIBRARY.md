---
title: "Go Library"
description: "Run a pgmi deployment from your own Go program with pkg/pgmi/deploy."
weight: 135
---

# Use pgmi as a Go library

pgmi is a CLI and a Go library. A devops tool written in Go can run a deploy
itself, with the same file discovery, session and execution contract as
`pgmi deploy`:

```bash
go get github.com/vvka-141/pgmi
```

```go
import (
	"github.com/vvka-141/pgmi/pkg/pgmi"
	"github.com/vvka-141/pgmi/pkg/pgmi/deploy"
)

result, err := deploy.New().Deploy(ctx, pgmi.DeploymentConfig{
	SourcePath:          "./db",
	ConnectionString:    os.Getenv("DATABASE_URL"),
	MaintenanceDatabase: "postgres",
	DatabaseName:        "app",
	Parameters:          map[string]string{"env": "dev"},
})
if err != nil {
	log.Printf("%s (exit code %d)", pgmi.FormatError(err), pgmi.ExitCodeForError(err))
}
```

- `pgmi.DeploymentConfig` takes what the CLI flags resolve to: a connection
  string, the target and maintenance databases, parameters, `Overwrite`,
  `Force`. It has no timeout: set a deadline on the context passed to
  `Deploy` (`context.WithTimeout`); a deploy that runs past it fails with an
  error `pgmi.ExitCodeForError` maps to 16, as the CLI's `--timeout` does. Nothing reads `pgmi.yaml` or
  `PGMI_PARAM_*` for you. The standard `PG*` variables do apply, through
  pgx, to any field the connection string leaves out: `PGUSER`,
  `PGPASSWORD`, `PGPASSFILE`, `PGCONNECT_TIMEOUT`, `PGSSLMODE`, `PGSSLKEY`,
  `PGSSLCERT`, `PGSSLCERTMODE`, `PGSSLSNI`, `PGSSLROOTCERT`, `PGSSLPASSWORD`,
  `PGSSLNEGOTIATION`, `PGTARGETSESSIONATTRS`, `PGSERVICE`, `PGSERVICEFILE`,
  `PGTZ`, `PGOPTIONS`, `PGMINPROTOCOLVERSION`, `PGMAXPROTOCOLVERSION`,
  `PGCHANNELBINDING` and `PGREQUIREAUTH`. `PGHOST`, `PGPORT` and `PGDATABASE`
  do not (unlike the CLI): an omitted host, port or database becomes
  `localhost`, `5432` and `postgres` before pgx sees the string, and
  `PGAPPNAME` does not either (pgmi names the session `pgmi`). pgmi itself
  reads `PGSSLMODE` for Azure and AWS token auth. A program that must not
  inherit them, say a CI job that sets `PG*` for another tool, puts the
  fields in the connection string or unsets the variables.
- `pgmi.DeployResult` says how far the deploy got, also on failure: files
  loaded, execution units and how many committed, the unit that failed,
  whether the database was created. `pgmi.ExitCodeForError` maps the error to
  the [CLI's exit codes](CLI.md).
- An overwrite of an existing database is refused unless `Force` is set or
  `deploy.WithApprover` decides. The library never prompts.
- `deploy.WithLogger` receives pgmi's progress messages, connection retries
  and warnings; the default discards them.
- `deploy.WithNoticeHandler` receives the PostgreSQL notices `deploy.sql`
  raises, as `(severity, message, detail, hint)` with the unlocalized
  severity; without it they are discarded. Each Deployer has its own, so
  concurrent deploys never mix their notices; one Deployer's handler is
  called concurrently when its deploys overlap.
- A nil logger or notice handler, or an empty `deploy.WithVersion`, keeps the
  default.

The API is pre-1.0 and may change between minor versions.
