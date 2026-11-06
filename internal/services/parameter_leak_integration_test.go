package services

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// docs/SECURITY.md promises that no parameter name, value, or hint about
// content ever appears in pgmi's own output. Nothing checked it, and the
// failure path is where such a promise usually breaks: pgmi keeps the exact
// text of each execution unit so PostgreSQL error positions resolve to a line,
// and it echoes the offending source line psql-style.
//
// A parameter reaches SQL through current_setting('pgmi.key'), evaluated server
// side, so its value is never part of the statement text pgmi holds. The tempting
// "simplification" of interpolating parameters before sending would defeat that
// everywhere at once.
//
// SQL that expands a value with format() inside EXECUTE is the boundary: a
// runtime error carries the expanded statement as CONTEXT. pgmi redacts a
// PASSWORD literal there; any other secret must be wrapped by the SQL itself
// (docs/SECURITY.md), as the advanced template does for its admin role.
//
// A literal written into deploy.sql IS echoed, exactly as psql echoes it:
// suppressing the offending line would make every syntax error undiagnosable.
func TestParameterValuesNeverReachDeployErrors(t *testing.T) {
	const sentinel = "SuperSecret_Sentinel_9137"

	connString := requireTestDB(t)
	testDB := "pgmi_itest_param_leak"
	cleanup := createTestDB(t, connString, testDB)
	defer cleanup()

	pool := connectToTestDB(t, connString, testDB)
	defer pool.Close()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	l := loader.NewLoader()
	if err := l.LoadFilesIntoSession(ctx, conn, nil); err != nil {
		t.Fatalf("load files: %v", err)
	}
	if err := l.LoadParametersIntoSession(ctx, conn, map[string]string{
		"db_password": sentinel,
	}); err != nil {
		t.Fatalf("load parameters: %v", err)
	}

	// The value must genuinely be reachable, or the test proves nothing by
	// finding no leak.
	var got string
	if err := conn.QueryRow(ctx, `SELECT current_setting('pgmi.db_password')`).Scan(&got); err != nil {
		t.Fatalf("read back parameter: %v", err)
	}
	if got != sentinel {
		t.Fatalf("parameter did not load: got %q", got)
	}

	// A syntax error carries no statement context; a runtime error inside
	// EXECUTE carries the expanded statement, password literal included, as
	// CONTEXT. Both must reach the user without the value.
	for _, tc := range []struct {
		name, sqlstate, deploySQL string
	}{
		{"syntax error", "42601", `
DO $$
BEGIN
    EXECUTE format(
        'CREATE ROLE leak_probe_role LOGIN PASSWORD %L NOSUPERUSER TOTALLY_BOGUS_KEYWORD',
        current_setting('pgmi.db_password'));
END $$;
`},
		{"runtime error", "42710", `
DO $$
BEGIN
    EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', current_user, current_setting('pgmi.db_password'));
END $$;
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newExecService().executeDeploySQL(ctx, conn, scanner.Result{DeploySQL: tc.deploySQL})
			if err == nil {
				t.Fatal("deploy succeeded; it must fail for this to exercise the error path")
			}
			formatted := pgmi.FormatError(err)
			d := pgmi.NewErrorDetail(err)

			for _, channel := range []struct{ name, text string }{
				{"error string", err.Error()},
				{"FormatError (stderr / MCP text)", formatted},
				{"--json message", d.Message},
				{"--json detail", d.Detail},
				{"--json where", d.Where},
			} {
				if strings.Contains(channel.text, sentinel) {
					t.Errorf("the parameter value appears in the %s:\n%s", channel.name, channel.text)
				}
			}

			// Guard the guard: a deploy error that carries no diagnostics at all
			// would pass the check above while telling the user nothing.
			if !strings.Contains(formatted, tc.sqlstate) {
				t.Errorf("the error lost its SQLSTATE, so the absence of the secret proves nothing:\n%s", formatted)
			}
		})
	}
}
