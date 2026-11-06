package scaffold_test

import (
	"context"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// Sign-in under concurrency needs two open transactions at once, which the
// deploy's own tests cannot have.
func TestAdvancedTemplate_LoginSessionsUnderConcurrency(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, nil)

	const db = "pgmi_advanced_login_race"
	defer testhelpers.CleanupTestDB(t, connString, db)
	if err := deployAdvanced(t, connString, db, projectPath, true, map[string]string{"env": "dev"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	pool := testhelpers.GetTestPool(t, connString, db)
	a := jobSession(t, pool)
	b := jobSession(t, pool)

	const signIn = `SELECT api.open_dev_login_session('dev', $1, $2, 'Race')`

	t.Run("simultaneous first sign-ins for one login make one person", func(t *testing.T) {
		principalsBefore := scanInt(t, a, `SELECT count(*) FROM membership.principal`)
		if err := call(a, `BEGIN`); err != nil {
			t.Fatal(err)
		}
		var tokenA *string
		if err := a.QueryRow(ctx, signIn, "race-1", "race@example.test").Scan(&tokenA); err != nil {
			t.Fatalf("session A signs in: %v", err)
		}

		type result struct {
			token *string
			err   error
		}
		done := make(chan result, 1)
		go func() {
			var tok *string
			err := b.QueryRow(ctx, signIn, "race-1", "race@example.test").Scan(&tok)
			done <- result{tok, err}
		}()
		time.Sleep(300 * time.Millisecond)
		if err := call(a, `COMMIT`); err != nil {
			t.Fatal(err)
		}
		r := <-done
		if r.err != nil {
			t.Fatalf("session B's sign-in must wait for A and then succeed, got %v", r.err)
		}
		if tokenA == nil || r.token == nil {
			t.Fatalf("both sign-ins must return a handle: A=%v B=%v", tokenA, r.token)
		}
		if n := scanInt(t, a, `SELECT count(*) FROM membership.user_principal WHERE email = 'race@example.test'`); n != 1 {
			t.Errorf("one login must make one person, got %d", n)
		}
		if n := scanInt(t, a, `SELECT count(*) FROM membership.principal`) - principalsBefore; n != 1 {
			t.Errorf("one new principal expected, got %d (an orphan was left behind)", n)
		}
	})

	t.Run("a held login session does not block a second request", func(t *testing.T) {
		var token string
		if err := a.QueryRow(ctx, signIn, "slide-1", "slide@example.test").Scan(&token); err != nil {
			t.Fatalf("sign in: %v", err)
		}
		if _, err := a.Exec(ctx, `UPDATE membership.login_session SET last_seen_at = now() - interval '10 minutes'
			WHERE token_hash = membership.fn_session_token_hash($1)`, token); err != nil {
			t.Fatal(err)
		}
		if err := call(a, `BEGIN`); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Exec(ctx, `SELECT internal.resolve_session_context($1, NULL)`, token); err != nil {
			t.Fatalf("session A resolves: %v", err)
		}
		if err := call(b, `SET lock_timeout = '2s'`); err != nil {
			t.Fatal(err)
		}
		_, err := b.Exec(ctx, `SELECT internal.resolve_session_context($1, NULL)`, token)
		_ = call(a, `COMMIT`)
		if err != nil {
			t.Fatalf("a second request on the same session must not queue behind the first: %v", err)
		}
	})
}

// A slug taken between a create's probe and its insert answers what the probe
// would have: 422 slug_taken, never the dispatcher's 409 for a unique violation.
func TestAdvancedTemplate_RacedOrganizationSlugIs422(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, nil)

	const db = "pgmi_advanced_slug_race"
	defer testhelpers.CleanupTestDB(t, connString, db)
	if err := deployAdvanced(t, connString, db, projectPath, true, map[string]string{"env": "dev"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	pool := testhelpers.GetTestPool(t, connString, db)
	a := jobSession(t, pool)
	b := jobSession(t, pool)

	const create = `SELECT r.status_code, convert_from(r.content, 'UTF8')::jsonb->>'code'
		FROM api.rest_invoke('POST', '/organizations',
			extensions.hstore(ARRAY['x-login-session', 'content-type'], ARRAY[$1, 'application/json']),
			convert_to('{"name": "Race One", "slug": "race-one"}', 'UTF8')) r`
	var tokenA, tokenB string
	if err := a.QueryRow(ctx, `SELECT api.open_dev_login_session('dev', 'slug-a', 'slug-a@example.test', 'A')`).Scan(&tokenA); err != nil {
		t.Fatal(err)
	}
	if err := b.QueryRow(ctx, `SELECT api.open_dev_login_session('dev', 'slug-b', 'slug-b@example.test', 'B')`).Scan(&tokenB); err != nil {
		t.Fatal(err)
	}

	if err := call(a, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	var status int
	var code *string
	if err := a.QueryRow(ctx, create, tokenA).Scan(&status, &code); err != nil || status != 201 {
		t.Fatalf("session A creates race-one: %d %v %v", status, code, err)
	}
	type result struct {
		status int
		code   *string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		r.err = b.QueryRow(ctx, create, tokenB).Scan(&r.status, &r.code)
		done <- r
	}()
	time.Sleep(300 * time.Millisecond)
	if err := call(a, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || r.status != 422 || r.code == nil || *r.code != "membership.organization.slug_taken" {
		t.Fatalf("the raced create must answer 422 slug_taken, got %d %v %v", r.status, r.code, r.err)
	}
}
