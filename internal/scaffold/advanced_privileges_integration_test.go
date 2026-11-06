package scaffold_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

const closedRoutinesProbe = `/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000030" idempotent="true">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
CREATE SCHEMA IF NOT EXISTS probe;
CREATE OR REPLACE FUNCTION core.closed_probe() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
CREATE OR REPLACE FUNCTION probe.opened_probe() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
GRANT EXECUTE ON FUNCTION probe.opened_probe() TO PUBLIC;
`

const closedRoutinesTestProbe = `CREATE FUNCTION core.test_file_probe() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
DO $$
BEGIN
    IF has_function_privilege('public', 'core.test_file_probe()', 'EXECUTE') THEN
        RAISE EXCEPTION 'TEST FAILED: a routine created by a test file is executable by PUBLIC';
    END IF;
END $$;
`

// A routine is callable only by a role it was granted to, whoever created it
// and wherever: the default privileges close what project and test files
// create, the end-of-deploy sweep closes what a file opened to PUBLIC in a
// schema the template does not know. Only the template's roles may connect.
// An extension an image pre-installed in public is moved into extensions.
func TestAdvancedTemplate_ClosedByDefault(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, map[string]string{
		"core/closed_probe.sql":               closedRoutinesProbe,
		"core/__test__/test_closed_probe.sql": closedRoutinesTestProbe,
	})
	approveSchema(t, projectPath, "probe")

	const db = "pgmi_advanced_closed"
	defer testhelpers.CleanupTestDB(t, connString, db)
	testhelpers.CreateTestDB(t, connString, db)
	pool := testhelpers.GetTestPool(t, connString, db)
	if _, err := pool.Exec(ctx, "CREATE EXTENSION hstore SCHEMA public; CREATE TABLE public.preinstalled (h public.hstore)"); err != nil {
		t.Fatalf("pre-install hstore in public: %v", err)
	}

	if err := deployAdvanced(t, connString, db, projectPath, false, map[string]string{"env": "dev"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	var hstoreSchema, columnType string
	if err := pool.QueryRow(ctx, `
		SELECT e.extnamespace::regnamespace::text, format_type(a.atttypid, a.atttypmod)
		FROM pg_extension e, pg_attribute a
		WHERE e.extname = 'hstore' AND a.attrelid = 'public.preinstalled'::regclass AND a.attname = 'h'`).Scan(&hstoreSchema, &columnType); err != nil {
		t.Fatalf("read hstore's schema: %v", err)
	}
	if hstoreSchema != "extensions" || columnType != "extensions.hstore" {
		t.Errorf("hstore in schema %s, column type %s; want extensions, extensions.hstore", hstoreSchema, columnType)
	}

	for _, fn := range []string{"core.closed_probe()", "probe.opened_probe()"} {
		var open bool
		if err := pool.QueryRow(ctx, "SELECT has_function_privilege('public', $1, 'EXECUTE')", fn).Scan(&open); err != nil {
			t.Fatalf("check %s: %v", fn, err)
		}
		if open {
			t.Errorf("%s is executable by PUBLIC", fn)
		}
	}

	var publicRoutines string
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(string_agg(p.oid::regprocedure::text, ', '), '')
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname IN ('api', 'core', 'common', 'internal', 'membership', 'probe')
		  AND has_function_privilege('public', p.oid, 'EXECUTE')`).Scan(&publicRoutines); err != nil {
		t.Fatalf("list routines executable by PUBLIC: %v", err)
	}
	if publicRoutines != "" {
		t.Errorf("routines executable by PUBLIC: %s", publicRoutines)
	}

	var publicConnect, publicTemp, adminConnect, adminTemp bool
	if err := pool.QueryRow(ctx, `
		SELECT has_database_privilege('public', current_database(), 'CONNECT'),
		       has_database_privilege('public', current_database(), 'TEMPORARY'),
		       has_database_privilege(current_database() || '_admin', current_database(), 'CONNECT'),
		       has_database_privilege(current_database() || '_admin', current_database(), 'TEMPORARY')`).Scan(
		&publicConnect, &publicTemp, &adminConnect, &adminTemp); err != nil {
		t.Fatalf("read database privileges: %v", err)
	}
	if publicConnect || publicTemp {
		t.Errorf("PUBLIC keeps CONNECT=%t TEMPORARY=%t on the database", publicConnect, publicTemp)
	}
	if !adminConnect || !adminTemp {
		t.Errorf("the admin role lacks CONNECT=%t TEMPORARY=%t", adminConnect, adminTemp)
	}
}

// The template needs its extensions in schema extensions. One installed
// elsewhere that cannot be moved is refused, by name, before any file runs.
func TestAdvancedTemplate_RefusesExtensionThatCannotMove(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	projectPath := scaffoldAdvanced(t, nil)

	const db = "pgmi_advanced_stuck_extension"
	defer testhelpers.CleanupTestDB(t, connString, db)
	testhelpers.CreateTestDB(t, connString, db)
	pool := testhelpers.GetTestPool(t, connString, db)
	if _, err := pool.Exec(ctx, `
		CREATE EXTENSION hstore SCHEMA public;
		UPDATE pg_extension SET extrelocatable = false WHERE extname = 'hstore'`); err != nil {
		t.Fatalf("plant a non-relocatable hstore in public: %v", err)
	}

	err := deployAdvanced(t, connString, db, projectPath, false, map[string]string{"env": "dev"})
	if err == nil {
		t.Fatal("deploy succeeded with hstore stuck in public")
	}
	if !strings.Contains(err.Error(), "Extension hstore is installed in schema public, cannot be moved") {
		t.Fatalf("want the refusal to name hstore and public, got: %v", err)
	}
	var schemas int
	if qerr := pool.QueryRow(ctx,
		"SELECT count(*) FROM pg_namespace WHERE nspname IN ('internal', 'core', 'api', 'membership')").Scan(&schemas); qerr != nil {
		t.Fatalf("count schemas: %v", qerr)
	}
	if schemas != 0 {
		t.Errorf("the refused deploy created %d schemas", schemas)
	}
}

const accessProbeCode = `/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000031" idempotent="true">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
CREATE OR REPLACE FUNCTION common.access_probe_fn() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
CREATE OR REPLACE FUNCTION core.access_probe_trg() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
DO $$ BEGIN EXECUTE format('GRANT EXECUTE ON FUNCTION common.access_probe_fn() TO %I', current_database() || '_kernel'); END $$;
`

const accessProbeStructure = `/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000032" idempotent="false">
  <sortKeys><key>900/001</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.access_probe (id int, note text);
SELECT pg_temp.exempt_from_rls('core.access_probe', 'test probe');
ALTER TABLE core.access_probe ENABLE ROW LEVEL SECURITY;
CREATE POLICY access_probe_read ON core.access_probe FOR SELECT USING (true);
CREATE TRIGGER access_probe_trg BEFORE INSERT ON core.access_probe FOR EACH ROW EXECUTE FUNCTION core.access_probe_trg();
DO $$
BEGIN
    EXECUTE format('GRANT SELECT ON core.access_probe TO %I', current_database() || '_admin');
    EXECUTE format('GRANT UPDATE (note) ON core.access_probe TO %I', current_database() || '_admin');
END $$;
`

func accessRevokeProbe(declared string) string {
	return fmt.Sprintf(`/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000033" idempotent="true">
  <sortKeys><key>900/002</key></sortKeys>
</pgmi-meta>
*/
DROP POLICY IF EXISTS access_probe_read ON core.access_probe;
DROP TRIGGER IF EXISTS access_probe_trg ON core.access_probe;
DO $$
DECLARE
    v_admin text := current_database() || '_admin';
BEGIN
    EXECUTE format('REVOKE SELECT ON core.access_probe FROM %%I', v_admin);
    EXECUTE format('REVOKE UPDATE (note) ON core.access_probe FROM %%I', v_admin);
    EXECUTE format('REVOKE EXECUTE ON FUNCTION common.access_probe_fn() FROM %%I', current_database() || '_kernel');
    %s
END $$;
`, declared)
}

// A deploy that takes away a grant, policy or trigger the database had before
// it fails and names each, unless a file declares the removal in the same deploy.
func TestAdvancedTemplate_AccessBaseline(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := scaffoldAdvanced(t, map[string]string{
		"core/access_probe_code.sql": accessProbeCode,
		"core/access_probe.sql":      accessProbeStructure,
	})

	const db = "pgmi_advanced_access"
	defer testhelpers.CleanupTestDB(t, connString, db)
	params := map[string]string{"env": "dev"}
	if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	writeProbes(t, projectPath, map[string]string{"core/revoke_probe.sql": accessRevokeProbe("")}, nil)
	err := deployAdvanced(t, connString, db, projectPath, false, params)
	if err == nil {
		t.Fatal("deploy succeeded after a file took away existing grants, a policy and a trigger")
	}
	admin := db + "_admin"
	for _, want := range []string{
		"took away grants, policies or triggers",
		"core.access_probe: grant SELECT to " + admin,
		"core.access_probe.note: grant UPDATE to " + admin,
		"common.access_probe_fn(): grant EXECUTE to " + db + "_kernel",
		"core.access_probe: policy access_probe_read",
		"core.access_probe: trigger access_probe_trg",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("want the error to contain %q, got: %v", want, err)
		}
	}

	writeProbes(t, projectPath, map[string]string{"core/revoke_probe.sql": accessRevokeProbe(`INSERT INTO pg_temp.access_removal VALUES
        ('core.access_probe', 'grant SELECT to ' || v_admin),
        ('core.access_probe.note', 'grant UPDATE to ' || v_admin),
        ('common.access_probe_fn()', 'grant EXECUTE to ' || current_database() || '_kernel'),
        ('core.access_probe', 'policy access_probe_read'),
        ('core.access_probe', 'trigger access_probe_trg');`)}, nil)
	if err := deployAdvanced(t, connString, db, projectPath, false, params); err != nil {
		t.Fatalf("deploy with the removals declared: %v", err)
	}
}

// approveSchema adds a schema without grants to the project's
// pg_temp.approved_schema_grant(), as a project that creates one does.
func approveSchema(t *testing.T, projectPath, schema string) {
	t.Helper()
	path := filepath.Join(projectPath, "deploy.sql")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const public = "           ('public',     NULL,                   NULL)"
	if !strings.Contains(string(src), public) {
		t.Fatal("deploy.sql has no public row in pg_temp.approved_schema_grant()")
	}
	patched := strings.Replace(string(src), public, fmt.Sprintf("           ('%s', NULL, NULL),\n%s", schema, public), 1)
	if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}
}
