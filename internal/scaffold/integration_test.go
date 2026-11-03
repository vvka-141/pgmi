package scaffold_test

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"testing"

	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/fakefs"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Floors, not exact counts: adding a test file to a template must not break
// this, but losing the whole tree to a discovery regression must.
var minExecutedTests = map[string]int{"basic": 1, "advanced": 25}

// Emitted per test by both the default callback and the advanced template's
// own observer, so counting them measures tests that actually ran.
var testStartPattern = regexp.MustCompile(`^\[pgmi\] Test: `)

// NOTICE lines a green non-verbose deploy may print outside its plan and test
// step headers: parameters, roles, catalog sync, the suite frame, the banner.
// Measured on a fresh cluster, where the role-creation lines appear; a reused
// cluster prints fewer, so a budget tuned there fails in CI.
var frameworkNoticeBudget = map[string]int{"basic": 6, "advanced": 40}

var (
	planStepPattern = regexp.MustCompile(`^\[pgmi\] Executing: `)
	testStepPattern = regexp.MustCompile(`^\[pgmi\] (?:Fixture|Test): (.+)$`)
	suiteEndPattern = regexp.MustCompile(`^\[pgmi\] Test suite passed`)
)

// TestTemplateDeployment tests all templates by initializing and deploying them
// using the Deployer interface (not CLI).
func TestTemplateDeployment(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	templates := []string{"basic", "advanced"}

	for _, templateName := range templates {
		t.Run(templateName, func(t *testing.T) {
			testTemplateDeployment(t, connString, templateName)
		})
	}
}

func testTemplateDeployment(t *testing.T, connString, templateName string) {
	ctx := context.Background()

	templateRoot := "templates/" + templateName
	embedFS := scaffold.GetTemplatesFS()
	efs := fakefs.NewEmbedFileSystem(embedFS, templateRoot)

	t.Logf("Testing %s template deployment from embedded FS...", templateName)

	// Step 1: Deploy directly from embedded filesystem with overwrite and force
	testDB := fmt.Sprintf("pgmi_test_%s", templateName)

	t.Logf("Deploying to database %s with overwrite=true, force=true...", testDB)

	notices := captureNotices(t)
	deployer := testhelpers.NewTestDeployerWithFS(t, efs)

	// Template-specific parameters
	params := map[string]string{"env": "dev"}
	if templateName == "advanced" {
		params["database_admin_password"] = "TestPassword123!"
		params["env"] = "test"
	}

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString: connString,
		DatabaseName:     testDB,
		SourcePath:       ".", // EmbedFileSystem root is already at template root
		Overwrite:        true,
		Force:            true,
		Parameters:       params,
		Verbose:          testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("First deployment failed for %s: %v", templateName, err)
	}
	t.Logf("✓ First deployment completed successfully")

	assertTestsExecuted(t, templateName, notices())
	if !testing.Verbose() {
		assertQuietDeploy(t, templateName, notices())
	}

	// Fingerprint the schema before redeploying. Asserting only that the second
	// deploy SUCCEEDS misses the failure that matters: DDL which accumulates
	// instead of erroring. An ADD CONSTRAINT without a name, a re-run CREATE
	// POLICY, an index created under a generated name — each quietly doubles on
	// every deploy while the deploy stays green.
	before := schemaFingerprint(t, connString, testDB)
	countsBefore := rowCounts(t, connString, testDB)

	// Step 2: Redeploy WITHOUT overwrite (idempotent test)
	t.Logf("Redeploying without overwrite (idempotent test)...")
	err = deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString: connString,
		DatabaseName:     testDB,
		SourcePath:       ".",
		Overwrite:        false,
		Force:            false,
		Parameters:       params,
		Verbose:          testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Idempotent redeployment failed for %s: %v", templateName, err)
	}
	t.Logf("✓ Idempotent redeployment completed successfully")

	if after := schemaFingerprint(t, connString, testDB); after != before {
		t.Errorf("redeploying changed the schema, so the second deploy is not idempotent\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
	assertNoDataDrift(t, countsBefore, rowCounts(t, connString, testDB))

	// Step 3: Verify deployment by querying the database
	defer testhelpers.CleanupTestDB(t, connString, testDB)
	verifyTemplateDeployment(t, connString, testDB, templateName)
}

// captureNotices redirects the PostgreSQL NOTICE stream for the duration of the
// test and returns an accessor for what has arrived so far.
func captureNotices(t *testing.T) func() []string {
	t.Helper()

	var (
		mu  sync.Mutex
		got []string
	)

	orig := db.NoticeHandler
	db.NoticeHandler = func(_, message, _, _ string) {
		mu.Lock()
		got = append(got, message)
		mu.Unlock()
	}
	t.Cleanup(func() { db.NoticeHandler = orig })

	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// assertTestsExecuted fails unless the deploy actually ran tests. A regression
// in __test__ discovery empties the plan; without this the deploy still
// succeeds and TestTemplateDeployment still passes.
func assertTestsExecuted(t *testing.T, templateName string, notices []string) {
	t.Helper()

	var count int
	for _, n := range notices {
		if testStartPattern.MatchString(n) {
			count++
		}
	}

	want, ok := minExecutedTests[templateName]
	if !ok {
		t.Fatalf("no test floor for template %q: add it to minExecutedTests, or this check silently passes with a floor of 0", templateName)
	}
	if count < want {
		t.Fatalf("%s deploy ran %d test(s), want >= %d", templateName, count, want)
	}
	t.Logf("✓ %s template ran %d test(s)", templateName, count)
}

// assertQuietDeploy keeps a green run readable by humans and agents; the
// advanced framework used to print ~650 NOTICE lines (PGMI-386). Step headers
// are free, a test step may add one line, the rest counts against the budget.
func assertQuietDeploy(t *testing.T, templateName string, notices []string) {
	t.Helper()

	budget, ok := frameworkNoticeBudget[templateName]
	if !ok {
		t.Fatalf("no framework NOTICE budget for template %q: add it to frameworkNoticeBudget", templateName)
	}
	framework, chatty := tallyNotices(notices)
	if framework > budget {
		t.Errorf("%s deploy printed %d framework NOTICE lines, want <= %d", templateName, framework, budget)
	}
	for _, step := range chatty {
		t.Errorf("%s printed more than one NOTICE line; demote the rest to DEBUG", step)
	}
}

// tallyNotices counts the lines that are neither a step header nor a test
// step's one line of its own, and names the test steps that printed more.
func tallyNotices(notices []string) (framework int, chatty []string) {
	var step string
	var lines int
	for _, n := range notices {
		switch {
		case planStepPattern.MatchString(n):
			step = ""
		case testStepPattern.MatchString(n):
			step, lines = testStepPattern.FindStringSubmatch(n)[1], 0
		case suiteEndPattern.MatchString(n):
			step = ""
			framework++
		case step != "":
			lines++
			if lines == 2 {
				chatty = append(chatty, step)
			}
		default:
			framework++
		}
	}
	return framework, chatty
}

func TestTallyNotices(t *testing.T) {
	run := func(extra ...string) []string {
		return append([]string{
			"[pgmi] Parameter: env = test",
			"[pgmi] Executing: ./lib/a.sql",
			"[pgmi] Executing: ./lib/b.sql",
			"[pgmi] Catalog data/x.json: 1 row(s)",
			"[pgmi] Test suite started",
			"[pgmi] Fixture: ./__test__/_setup.sql",
			"[pgmi] Test: ./__test__/test_a.sql",
			"  + a holds",
			"[pgmi] Test: ./__test__/test_b.sql",
		}, append(extra, "[pgmi] Test suite passed", "DONE")...)
	}

	framework, chatty := tallyNotices(run())
	if framework != 5 || len(chatty) != 0 {
		t.Errorf("quiet run: framework=%d chatty=%v, want 5 and none", framework, chatty)
	}

	_, chatty = tallyNotices(run("  + b holds", "  + b also holds"))
	if len(chatty) != 1 || chatty[0] != "./__test__/test_b.sql" {
		t.Errorf("a test step printing two lines: chatty=%v, want [./__test__/test_b.sql]", chatty)
	}
}

// verifyTemplateDeployment performs template-specific verification queries
func verifyTemplateDeployment(t *testing.T, connString, dbName, templateName string) {
	t.Helper()

	ctx := context.Background()
	pool := testhelpers.GetTestPool(t, connString, dbName)

	// Basic sanity check - ensure we can query the database
	var result int
	err := pool.QueryRow(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("Failed to query deployed database: %v", err)
	}
	if result != 1 {
		t.Errorf("Expected result=1, got %d", result)
	}

	// Template-specific verification
	switch templateName {
	case "basic":
		var state string
		var runOnce int
		err = pool.QueryRow(ctx, `SELECT (SELECT state FROM project_view WHERE name = 'Demo'), (SELECT count(*) FROM deployment.run_once)`).Scan(&state, &runOnce)
		if err != nil {
			t.Fatalf("query the basic deploy: %v", err)
		}
		if state != "empty" || runOnce != 2 {
			t.Errorf("basic deploy: Demo project state %q and %d run-once file(s) recorded, want \"empty\" and 2", state, runOnce)
		}

	case "advanced":
		if _, err := pool.Exec(ctx, advancedPostconditions); err != nil {
			t.Error(err)
		} else {
			t.Logf("✓ Advanced template post-conditions satisfied")
		}
	}
}

// advancedPostconditions asserts the reference application was actually built.
// It runs out of band — fresh connection, after the deploy committed — which is
// what the template's own __test__ scripts cannot do; everything else about it
// belongs in SQL, so the predicates and their wording live here rather than in
// Go. Role names default to <database>_<suffix> (advanced/deploy.sql) and the
// test supplies no override, so current_database() reconstructs them.
//
// The execution log is deliberately not checked here:
// lib/__test__/test_migrations_tracking.sql already owns that assertion.
const advancedPostconditions = `
DO $$
DECLARE
    v_problems text[] := '{}';
    v_handlers bigint;
BEGIN
    v_problems := v_problems || ARRAY(
        SELECT format('schema %s absent', s)
        FROM unnest(ARRAY['api', 'core', 'membership', 'internal']) AS s
        WHERE NOT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = s)
    );

    v_problems := v_problems || ARRAY(
        SELECT format('role %s uncreated', current_database() || s)
        FROM unnest(ARRAY['_owner', '_admin', '_api', '_kernel']) AS s
        WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_database() || s)
    );

    IF to_regclass('api.handler') IS NULL THEN
        v_problems := v_problems || 'api.handler absent - the route registry was never created'::text;
    ELSE
        SELECT count(*) INTO v_handlers FROM api.handler WHERE deleted_at IS NULL;
        IF v_handlers = 0 THEN
            v_problems := v_problems || 'route registry empty - the deploy registered no handlers'::text;
        END IF;
    END IF;

    IF cardinality(v_problems) > 0 THEN
        RAISE EXCEPTION 'advanced deploy post-conditions failed: %',
            array_to_string(v_problems, '; ');
    END IF;
END $$;
`
