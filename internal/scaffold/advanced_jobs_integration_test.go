package scaffold_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

func jobSession(t *testing.T, pool *pgxpool.Pool) *pgx.Conn {
	t.Helper()
	conn, err := pgx.ConnectConfig(context.Background(), pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatalf("open a session: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func call(conn *pgx.Conn, sql string) error {
	_, err := conn.Exec(context.Background(), sql, pgx.QueryExecModeSimpleProtocol)
	return err
}

func scanInt(t *testing.T, conn *pgx.Conn, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

const expiredRest = `SELECT count(*) FROM api.rest_exchange WHERE enqueued_at < now() - api.exchange_retention()`

// The job contract needs what the deploy's own tests cannot have: commits
// between batches and a second session racing for the same job.
func TestAdvancedTemplate_BackgroundJobs(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, nil)

	const db = "pgmi_advanced_jobs"
	defer testhelpers.CleanupTestDB(t, connString, db)
	if err := deployAdvanced(t, connString, db, projectPath, true, map[string]string{"env": "dev"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	pool := testhelpers.GetTestPool(t, connString, db)
	a := jobSession(t, pool)
	b := jobSession(t, pool)

	if err := call(a, `
		INSERT INTO api.rest_exchange (object_id, enqueued_at, handler_object_id, request, response, completed_at)
		SELECT gen_random_uuid(), now() - api.exchange_retention() - make_interval(hours => n),
		       (SELECT object_id FROM api.handler WHERE handler_function_name = 'hello_world'),
		       ROW('GET', '/t', ''::extensions.hstore, NULL::bytea, '{}'::jsonb)::api.rest_request,
		       ROW(200, ''::extensions.hstore, NULL::bytea)::api.http_response, now()
		FROM generate_series(1, 7) n`); err != nil {
		t.Fatalf("insert expired exchanges: %v", err)
	}

	t.Run("a second run returns without work", func(t *testing.T) {
		var run string
		if err := a.QueryRow(ctx, `SELECT api.job_run_started('purge_exchanges')::text`).Scan(&run); err != nil {
			t.Fatalf("session A starts a run: %v", err)
		}
		if err := call(b, `CALL api.job_purge_exchanges(2)`); err != nil {
			t.Fatalf("session B CALL: %v", err)
		}
		if n := scanInt(t, b, expiredRest); n != 7 {
			t.Fatalf("session B purged while A held the job: %d of 7 expired exchanges left", n)
		}
		if n := scanInt(t, b, `SELECT count(*) FROM internal.job_run_log WHERE job_name = 'purge_exchanges'`); n != 1 {
			t.Fatalf("session B logged a run while A held the job: %d runs", n)
		}
		if _, err := a.Exec(ctx, `SELECT api.job_run_finished($1::uuid, 0, 0)`, run); err != nil {
			t.Fatalf("session A finishes: %v", err)
		}
	})

	t.Run("max batches caps a run and the log records it", func(t *testing.T) {
		if err := call(b, `CALL api.job_purge_exchanges(2, 2)`); err != nil {
			t.Fatalf("CALL: %v", err)
		}
		if n := scanInt(t, b, expiredRest); n != 3 {
			t.Fatalf("two batches of 2 must leave 3 of 7 expired exchanges, left %d", n)
		}
		var outcome string
		var rows, batches int64
		var finished bool
		if err := b.QueryRow(ctx, `
			SELECT outcome, rows_affected, batches, finished_at >= started_at
			FROM internal.job_run_log WHERE job_name = 'purge_exchanges' ORDER BY started_at DESC LIMIT 1`).
			Scan(&outcome, &rows, &batches, &finished); err != nil {
			t.Fatalf("read the run log: %v", err)
		}
		if outcome != "succeeded" || rows != 4 || batches != 2 || !finished {
			t.Fatalf("run log: outcome %s, rows %d, batches %d, finished %v; want succeeded, 4, 2, true", outcome, rows, batches, finished)
		}
	})

	t.Run("a batch returning 0 ends the run", func(t *testing.T) {
		if err := call(b, `CALL api.job_purge_exchanges(2)`); err != nil {
			t.Fatalf("CALL: %v", err)
		}
		if n := scanInt(t, b, expiredRest); n != 0 {
			t.Fatalf("an unlimited run must purge every expired exchange, %d left", n)
		}
		if n := scanInt(t, b, `SELECT batches FROM internal.job_run_log WHERE job_name = 'purge_exchanges' ORDER BY started_at DESC LIMIT 1`); n != 3 {
			t.Fatalf("3 rows in batches of 2 take 2 batches and an empty one, logged %d", n)
		}
	})

	t.Run("a failed run blocks others until its session ends", func(t *testing.T) {
		if err := call(a, `
			CREATE FUNCTION api.job_probe_fail_batch(int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe failure'; END $$;
			CREATE FUNCTION api.job_probe_ok_batch(int) RETURNS int LANGUAGE sql AS $$ SELECT 0 $$;`); err != nil {
			t.Fatalf("create probe batches: %v", err)
		}
		failing := jobSession(t, pool)
		err := call(failing, `CALL api.job_run('probe', 'api.job_probe_fail_batch(int)')`)
		if err == nil || !strings.Contains(err.Error(), "probe failure") {
			t.Fatalf("the failing batch must fail the run, got %v", err)
		}
		if err := call(b, `CALL api.job_run('probe', 'api.job_probe_ok_batch(int)')`); err != nil {
			t.Fatalf("CALL while the failed session lives: %v", err)
		}
		if n := scanInt(t, b, `SELECT count(*) FROM internal.job_run_log WHERE job_name = 'probe'`); n != 1 {
			t.Fatalf("a run started while the failed session holds the job: %d runs", n)
		}

		pid := failing.PgConn().PID()
		_ = failing.Close(ctx)
		deadline := time.Now().Add(10 * time.Second)
		for scanInt(t, b, `SELECT count(*) FROM pg_stat_activity WHERE pid = $1`, int64(pid)) > 0 {
			if time.Now().After(deadline) {
				t.Fatal("the failed session did not end")
			}
			time.Sleep(50 * time.Millisecond)
		}

		if err := call(b, `CALL api.job_run('probe', 'api.job_probe_ok_batch(int)')`); err != nil {
			t.Fatalf("CALL after the failed session ended: %v", err)
		}
		if n := scanInt(t, b, `
			SELECT count(*) FROM internal.job_run_log
			WHERE job_name = 'probe' AND outcome IS NOT DISTINCT FROM CASE WHEN backend_pid = $1 THEN 'abandoned' ELSE 'succeeded' END`, int64(pid)); n != 2 {
			t.Fatalf("want the failed run abandoned and the new one succeeded, %d of 2 rows match", n)
		}
	})
}

// The jobs login is a documented option: a default deploy does not create
// it, and once created as the README shows, the next deploy lets it run jobs
// as its own login, attributed to its service principal.
func TestAdvancedTemplate_JobsLogin(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, nil)

	const db = "pgmi_advanced_jobs_login"
	const jobs = db + "_jobs"
	params := map[string]string{"env": "dev"}
	server := testhelpers.GetTestPool(t, connString, "postgres")
	defer func() { _, _ = server.Exec(ctx, `DROP ROLE IF EXISTS `+jobs) }()
	defer testhelpers.CleanupTestDB(t, connString, db)
	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	pool := testhelpers.GetTestPool(t, connString, db)
	owner := jobSession(t, pool)

	if n := scanInt(t, owner, `SELECT count(*) FROM pg_roles WHERE rolname = $1`, jobs); n != 0 {
		t.Fatalf("a default deploy created %s", jobs)
	}
	if err := call(owner, `CREATE ROLE `+jobs+` LOGIN PASSWORD 'jobs-secret' CONNECTION LIMIT 2`); err != nil {
		t.Fatalf("create the jobs login: %v", err)
	}
	var service string
	if err := owner.QueryRow(ctx, `SELECT (membership.fn_register_service_principal('service.jobs', 'Background jobs', $1)).id`, jobs).Scan(&service); err != nil || service != "service.jobs" {
		t.Fatalf("register the service principal: %q, %v", service, err)
	}
	if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
		t.Fatalf("redeploy with the jobs login: %v", err)
	}

	cfg := pool.Config().ConnConfig.Copy()
	cfg.User = jobs
	cfg.Password = "jobs-secret"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect as the jobs login: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if err := call(conn, `CALL api.job_purge_exchanges()`); err != nil {
		t.Fatalf("the jobs login runs the retention job: %v", err)
	}
	if n := scanInt(t, owner, `
		SELECT count(*) FROM internal.job_run_log l
		JOIN membership.service_principal s ON s.database_role = l.database_role
		WHERE l.job_name = 'purge_exchanges' AND l.outcome = 'succeeded' AND s.id = 'service.jobs'`); n != 1 {
		t.Fatalf("want one succeeded run attributed to service.jobs, got %d", n)
	}
	if err := call(conn, `SELECT api.catalog_version()`); err == nil || !strings.Contains(err.Error(), "42501") {
		t.Fatalf("the jobs login must not reach the gateway, got %v", err)
	}
}
