package scaffold_test

import (
	"context"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// The template's tests run on every deploy, beside the application's own
// people. A person signed in the way the docs show must not collide with a
// test fixture on the next deploy.
func TestAdvancedTemplate_RedeploysOntoSignedInPeople(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := scaffoldAdvanced(t, nil)
	const db = "pgmi_advanced_populated"
	defer testhelpers.CleanupTestDB(t, connString, db)
	params := map[string]string{"env": "dev"}

	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	pool := testhelpers.GetTestPool(t, connString, db)
	ctx := context.Background()
	var status int
	if err := pool.QueryRow(ctx, `
		SELECT (api.rest_invoke('POST', '/organizations',
		        extensions.hstore(ARRAY['x-login-session', api.open_dev_login_session('dev', 'alice', 'alice@example.com', 'Alice'),
		                                'content-type', 'application/json']),
		        convert_to('{"name": "Acme Corp", "slug": "alices-projects"}', 'UTF8'))).status_code`).Scan(&status); err != nil {
		t.Fatalf("sign alice in and create her organization: %v", err)
	}
	if status != 201 {
		t.Fatalf("self-serve POST /organizations answered %d, want 201", status)
	}

	if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
		t.Fatalf("redeploy onto a database with a signed-in person: %v", err)
	}
}
