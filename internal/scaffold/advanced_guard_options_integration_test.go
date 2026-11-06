package scaffold_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

var guardOptions = []string{
	"strict_fk_index", "rls_index_method", "plpgsql_check", "sql_lint", "naming",
	"handler_reads_views", "view_column_comments", "trigger_allowlist",
}

// enableGuardOptions switches the named options on in the project's
// pg_temp.guard_enabled(), as a project does.
func enableGuardOptions(t *testing.T, projectPath string, names ...string) {
	t.Helper()
	path := filepath.Join(projectPath, "deploy.sql")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(src)
	for _, name := range names {
		row := regexp.MustCompile(`(\('` + name + `',\s*)false\)`)
		if !row.MatchString(out) {
			t.Fatalf("deploy.sql has no switch for guard option %s", name)
		}
		out = row.ReplaceAllString(out, "${1}true)")
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

func guardProbe(id, idempotent, sortKey, body string) string {
	return "/*\n<pgmi-meta id=\"" + id + "\" idempotent=\"" + idempotent + "\">\n  <sortKeys><key>" + sortKey +
		"</key></sortKeys>\n</pgmi-meta>\n*/\n" + body
}

// Every option is off in the template; switched on, each is green on the
// template's own objects and fails a deploy holding the object it forbids.
func TestAdvancedTemplate_GuardOptions(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	params := map[string]string{"env": "dev"}

	t.Run("all on, template green", func(t *testing.T) {
		projectPath := scaffoldAdvanced(t, nil)
		enableGuardOptions(t, projectPath, guardOptions...)
		const db = "pgmi_advanced_guard_options"
		defer testhelpers.CleanupTestDB(t, connString, db)
		if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
			t.Fatalf("deploy with every guard option on: %v", err)
		}
	})

	tests := []struct {
		option string
		files  map[string]string
		want   string
	}{
		{"strict_fk_index", map[string]string{
			"core/strict_fk.sql": guardProbe("ffffffff-0451-4000-8000-000000000101", "false", "900/000", `
CREATE TABLE core.guard_parent (object_id uuid PRIMARY KEY);
CREATE TABLE core.guard_child (parent_id uuid REFERENCES core.guard_parent ON DELETE CASCADE, deleted_at timestamptz);
CREATE INDEX ix_guard_child_live ON core.guard_child (parent_id) WHERE deleted_at IS NULL;
SELECT pg_temp.exempt_from_rls(t::regclass, 'test probe') FROM unnest(ARRAY['core.guard_parent', 'core.guard_child']) t;
`)}, "core.guard_child (parent_id) -> core.guard_parent"},
		{"rls_index_method", map[string]string{
			"core/rls_index.sql": guardProbe("ffffffff-0451-4000-8000-000000000102", "false", "900/000", `
CREATE TABLE core.guard_tagged (organization_id uuid, tags text[]);
SELECT core.apply_org_rls('core.guard_tagged', p_has_created_by => false);
CREATE INDEX ix_guard_tagged_tags ON core.guard_tagged USING gin (tags);
`)}, "core.ix_guard_tagged_tags (gin on core.guard_tagged)"},
		{"plpgsql_check", map[string]string{
			"core/plpgsql_check.sql": guardProbe("ffffffff-0451-4000-8000-000000000103", "true", "900/000", `
CREATE OR REPLACE FUNCTION core.guard_misspelt() RETURNS uuid LANGUAGE plpgsql AS $f$
BEGIN
    RETURN (SELECT no_such_column FROM membership.organization LIMIT 1);
END $f$;
`)}, "core.guard_misspelt(): column \"no_such_column\" does not exist"},
		{"sql_lint", map[string]string{
			"core/sql_lint.sql": guardProbe("ffffffff-0451-4000-8000-000000000104", "true", "900/000", `
CREATE OR REPLACE FUNCTION core.guard_null_compare(p_id uuid) RETURNS bigint LANGUAGE sql AS $f$
    SELECT count(*) FROM membership.organization WHERE object_id = NULL
$f$;
`)}, "core.guard_null_compare(p_id uuid): NULL comparison"},
		{"naming", map[string]string{
			"core/naming.sql": guardProbe("ffffffff-0451-4000-8000-000000000105", "false", "900/000", `
CREATE TABLE core.guard_named (id int CONSTRAINT guard_named_pk PRIMARY KEY);
SELECT pg_temp.exempt_from_rls('core.guard_named', 'test probe');
`)}, "primary key core.guard_named_pk on guard_named (want _pkey$)"},
		{"handler_reads_views", map[string]string{
			"core/reads.sql": guardProbe("ffffffff-0451-4000-8000-000000000106", "false", "900/000", `
CREATE TABLE core.guard_fact (id int);
SELECT pg_temp.exempt_from_rls('core.guard_fact', 'test probe');
`),
			"api/reads.sql": guardProbe("ffffffff-0451-4000-8000-000000000107", "true", "900/001", `
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object('id', 'ffffffff-0451-4000-8000-000000000108', 'path', '/guard-facts', 'httpMethod', '^GET$',
                       'name', 'guard_facts', 'access', 'public', 'outputSchema', jsonb_build_object('type', 'object')),
    $b$BEGIN
        RETURN api.json_response(200, jsonb_build_object('n', (SELECT count(*) FROM core.guard_fact)));
    END;$b$);
`)}, "guard_facts reads core.guard_fact"},
		{"view_column_comments", map[string]string{
			"core/view.sql": guardProbe("ffffffff-0451-4000-8000-000000000109", "true", "900/000", `
CREATE OR REPLACE VIEW core.vw_guard_undocumented WITH (security_invoker = true) AS SELECT 1 AS answer;
`)}, "core.vw_guard_undocumented.answer"},
		{"trigger_allowlist", map[string]string{
			"core/trigger.sql": guardProbe("ffffffff-0451-4000-8000-000000000110", "false", "900/000", `
CREATE TABLE core.guard_triggered (id int);
SELECT pg_temp.exempt_from_rls('core.guard_triggered', 'test probe');
`),
			"core/trigger_code.sql": guardProbe("ffffffff-0451-4000-8000-000000000111", "true", "900/001", `
CREATE OR REPLACE FUNCTION core.guard_touch() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN RETURN NEW; END $f$;
CREATE OR REPLACE TRIGGER trg_guard_triggered BEFORE INSERT ON core.guard_triggered
    FOR EACH ROW EXECUTE FUNCTION core.guard_touch();
`)}, "core.guard_triggered trg_guard_triggered"},
	}

	for _, tt := range tests {
		t.Run(tt.option, func(t *testing.T) {
			if tt.option == "plpgsql_check" && !plpgsqlCheckAvailable(t, connString) {
				t.Skip("plpgsql_check is not installed on the server, or the test login is not a superuser")
			}
			projectPath := scaffoldAdvanced(t, tt.files)
			db := "pgmi_advanced_option_" + tt.option
			defer testhelpers.CleanupTestDB(t, connString, db)

			if err := deployAdvanced(t, connString, db, projectPath, true, params); err != nil {
				t.Fatalf("with %s off, the planted object must deploy: %v", tt.option, err)
			}
			enableGuardOptions(t, projectPath, tt.option)
			err := deployAdvanced(t, connString, db, projectPath, true, params)
			if err == nil {
				t.Fatalf("with %s on, a deploy holding the forbidden object succeeded", tt.option)
			}
			if want := "Deploy guard " + tt.option + ":"; !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want the error to contain %q and %q, got: %v", want, tt.want, err)
			}
		})
	}
}

func plpgsqlCheckAvailable(t *testing.T, connString string) bool {
	t.Helper()
	var ok bool
	if err := testhelpers.GetTestPool(t, connString, "postgres").QueryRow(context.Background(),
		`SELECT EXISTS (SELECT FROM pg_available_extensions WHERE name = 'plpgsql_check')
		    AND (SELECT rolsuper FROM pg_roles WHERE rolname = current_user)`).Scan(&ok); err != nil {
		t.Fatalf("probe for plpgsql_check: %v", err)
	}
	return ok
}
