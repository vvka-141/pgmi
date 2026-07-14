package scaffold_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/scaffold"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestAdvancedTemplate_SerializationFailureRetryContract drives a real write
// conflict — not a simulated one — through the REST gateway.
//
// Two concurrent REPEATABLE READ transactions update the same row via
// api.rest_invoke. The second is aborted by PostgreSQL with 40001. The gateway
// must let that SQLSTATE reach the client: flattened into a 500, a client cannot
// tell "your transaction lost a race, retry it" from "this handler is broken".
//
// The test also pins the reason catching 40001 is unsafe: the exception block's
// implicit savepoint rolls the failed statement back but leaves the transaction
// alive and committable, so a caught 40001 commits having silently dropped the
// handler's write.
func TestAdvancedTemplate_SerializationFailureRetryContract(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	testDB := "pgmi_test_retry_contract"
	deployAdvancedTemplate(t, connString, testDB)
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	pool := testhelpers.GetTestPool(t, connString, testDB)

	// A route whose handler reads-then-writes one row: the simplest shape that
	// two concurrent repeatable-read transactions cannot both commit.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.contended_counter (id int PRIMARY KEY, v int NOT NULL);
		INSERT INTO public.contended_counter VALUES (1, 0);
		-- rest_invoke is SECURITY DEFINER and runs as the deploy owner, not as the
		-- superuser that created this fixture table.
		GRANT ALL ON public.contended_counter TO PUBLIC;

		SELECT api.create_or_replace_rest_handler(
			jsonb_build_object(
				'id', 'ffffffff-0179-4000-8000-000000000001',
				'uri', '^/contend$',
				'httpMethod', '^POST$',
				'name', 'contend_test',
				'requiresAuth', false,
				'autoLog', false
			),
			$body$
		BEGIN
			UPDATE public.contended_counter SET v = v + 1 WHERE id = 1;
			RETURN api.json_response(200, jsonb_build_object('ok', true));
		END;
			$body$
		);
	`); err != nil {
		t.Fatalf("failed to set up the contended route: %v", err)
	}

	winner, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire winner conn: %v", err)
	}
	defer winner.Release()

	loser, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire loser conn: %v", err)
	}
	defer loser.Release()

	const invoke = `SELECT (api.rest_invoke('POST', '/contend')).status_code`

	// Winner takes the row lock and holds it.
	if _, err := winner.Exec(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ"); err != nil {
		t.Fatalf("winner BEGIN: %v", err)
	}
	var winnerStatus int
	if err := winner.QueryRow(ctx, invoke).Scan(&winnerStatus); err != nil {
		t.Fatalf("winner invoke: %v", err)
	}
	if winnerStatus != 200 {
		t.Fatalf("winner should have succeeded, got status %d", winnerStatus)
	}

	// Loser starts its transaction (taking its snapshot) and then blocks on the
	// row lock the winner holds.
	if _, err := loser.Exec(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ"); err != nil {
		t.Fatalf("loser BEGIN: %v", err)
	}
	if _, err := loser.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatalf("loser snapshot: %v", err)
	}

	loserResult := make(chan error, 1)
	go func() {
		var status int
		loserResult <- loser.QueryRow(ctx, invoke).Scan(&status)
	}()

	// Releasing the lock hands the loser a serialization failure.
	if _, err := winner.Exec(ctx, "COMMIT"); err != nil {
		t.Fatalf("winner COMMIT: %v", err)
	}

	err = <-loserResult
	if err == nil {
		t.Fatal("the losing transaction must fail: the gateway swallowed the serialization failure " +
			"and returned a response, so the client has no way to know it should retry")
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a PostgreSQL error carrying a SQLSTATE, got: %v", err)
	}
	if pgErr.Code != "40001" {
		t.Fatalf("expected SQLSTATE 40001 (serialization_failure) to reach the client, got %s: %s",
			pgErr.Code, pgErr.Message)
	}

	// Retry: a NEW transaction, therefore a new snapshot. This is the only thing
	// that converges — a savepoint would rewind the write but keep the stale
	// snapshot, and the retry would conflict identically, forever.
	if _, err := loser.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("loser ROLLBACK: %v", err)
	}
	if _, err := loser.Exec(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ"); err != nil {
		t.Fatalf("loser retry BEGIN: %v", err)
	}
	var retryStatus int
	if err := loser.QueryRow(ctx, invoke).Scan(&retryStatus); err != nil {
		t.Fatalf("retry on a fresh transaction should succeed, got: %v", err)
	}
	if retryStatus != 200 {
		t.Fatalf("retry should have returned 200, got %d", retryStatus)
	}
	if _, err := loser.Exec(ctx, "COMMIT"); err != nil {
		t.Fatalf("loser retry COMMIT: %v", err)
	}

	// Both writes landed: nothing was silently lost.
	var final int
	if err := pool.QueryRow(ctx, "SELECT v FROM public.contended_counter WHERE id = 1").Scan(&final); err != nil {
		t.Fatalf("read final counter: %v", err)
	}
	if final != 2 {
		t.Errorf("both increments must be durable after the retry; counter is %d, want 2 "+
			"(a value of 1 means a write was silently dropped)", final)
	}
}

func deployAdvancedTemplate(t *testing.T, connString, testDB string) {
	t.Helper()

	efs := filesystem.NewEmbedFileSystem(scaffold.GetTemplatesFS(), "templates/advanced")
	deployer := testhelpers.NewTestDeployerWithFS(t, efs)

	err := deployer.Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString: connString,
		DatabaseName:     testDB,
		SourcePath:       ".",
		Overwrite:        true,
		Force:            true,
		Parameters: map[string]string{
			"database_admin_password":    "TestPassword123!",
			"database_customer_password": "CustomerPassword123!",
			"env":                        "test",
		},
		Verbose: testing.Verbose(),
	})
	if err != nil {
		t.Fatalf("advanced template deployment failed: %v", err)
	}
}
