package scaffold_test

import (
	"os"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/preprocessor"
)

// The advanced template promises that declaring `object_id core.entity_id`
// gets created_at and deleted_at injected "by the deploy-end sweep"
// (lib/core/foundation.sql). That promise rests on one line in deploy.sql
// calling apply_entity_standards_all(), and nothing else in the suite would
// notice its absence:
//
//   - both shipped entity tables (api.handler, membership.api_key) call
//     apply_entity_table_standards inline, so they are unaffected;
//   - test_entity_standards.sql invokes the sweep itself, so it passes either
//     way.
//
// Delete the call and only a user's own table loses its columns — silently at
// deploy time, then loudly at runtime when the soft-delete views query
// deleted_at.
func TestAdvancedDeploySQLWiresTheEntityStandardsSweep(t *testing.T) {
	body, err := os.ReadFile("templates/advanced/deploy.sql")
	if err != nil {
		t.Fatalf("read advanced deploy.sql: %v", err)
	}
	// Comments first: deploy.sql's header documents "CALL pgmi_test()" dozens
	// of lines above the real call, and matching that instead of the statement
	// made this test report an ordering violation that does not exist.
	deploySQL := preprocessor.NewCommentStripper().Strip(string(body))

	const sweep = "pg_temp.apply_entity_standards_all()"
	sweepAt := strings.Index(deploySQL, sweep)
	if sweepAt == -1 {
		t.Fatalf("deploy.sql never calls %s — a user table declaring "+
			"object_id core.entity_id would not get created_at/deleted_at", sweep)
	}

	// Ordering is load-bearing in both directions.
	deployAt := strings.Index(deploySQL, "pg_temp.deploy()")
	if deployAt == -1 {
		t.Fatal("deploy.sql never calls pg_temp.deploy()")
	}
	if sweepAt < deployAt {
		t.Error("the sweep runs before pg_temp.deploy(), so it cannot see tables the " +
			"deployment just created")
	}

	testsAt := strings.Index(deploySQL, "CALL pgmi_test(")
	if testsAt == -1 {
		t.Fatal("deploy.sql never calls pgmi_test()")
	}
	if sweepAt > testsAt {
		t.Error("the sweep runs after the test gate, so tests see un-swept tables and a " +
			"conformance failure would abort the deploy only after the tests claimed it was fine")
	}
}
