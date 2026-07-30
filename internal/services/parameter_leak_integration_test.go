package services

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// docs/SECURITY.md promises that no parameter name, value, or hint about
// content ever appears in pgmi's own output. Nothing checked it, and the
// failure path is where such a promise usually breaks: pgmi keeps the exact
// text of each execution unit so PostgreSQL error positions resolve to a line,
// and it echoes the offending source line psql-style.
//
// The promise holds structurally rather than by redaction. A parameter reaches
// SQL through current_setting('pgmi.key'), evaluated server side, so its value
// is never part of the statement text pgmi holds — there is nothing to leak
// even when that statement fails. This pins that, because the tempting
// "simplification" is to interpolate parameters into the SQL before sending it,
// which would defeat the guarantee everywhere at once.
//
// The boundary is a literal written into deploy.sql. That IS echoed, exactly as
// psql echoes it, and is documented in docs/SECURITY.md rather than redacted:
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

	// A role option that does not exist, so the statement carrying the secret
	// fails with 42601 after the value has been interpolated server side.
	const deploySQL = `
DO $$
BEGIN
    EXECUTE format(
        'CREATE ROLE leak_probe_role LOGIN PASSWORD %L NOSUPERUSER TOTALLY_BOGUS_KEYWORD',
        current_setting('pgmi.db_password'));
END $$;
`

	svc := newServiceWithReadContent(deploySQL)
	_, err = svc.executeDeploySQL(ctx, conn, "/fake/deploy.sql")
	if err == nil {
		t.Fatal("deploy succeeded; it must fail for this to exercise the error path")
	}

	for _, channel := range []struct{ name, text string }{
		{"error string", err.Error()},
		{"FormatError (stderr / --json / MCP text)", pgmi.FormatError(err)},
	} {
		if strings.Contains(channel.text, sentinel) {
			t.Errorf("the parameter value appears in the %s:\n%s", channel.name, channel.text)
		}
	}

	// Guard the guard: a deploy error that carries no diagnostics at all would
	// pass the check above while telling the user nothing.
	if !strings.Contains(pgmi.FormatError(err), "42601") {
		t.Errorf("the error lost its SQLSTATE, so the absence of the secret proves nothing:\n%s",
			pgmi.FormatError(err))
	}
}
