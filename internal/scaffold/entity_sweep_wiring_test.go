package scaffold_test

import (
	"os"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/preprocessor"
)

// The advanced template promises that declaring `object_id core.entity_id`
// gets created_at and deleted_at injected "by the deploy-end sweep"
// (lib/core/foundation.sql). That promise rests on one line in
// deploy/finalize.sql calling apply_entity_standards_all(), and nothing else in the suite would
// notice its absence:
//
//   - the shipped entity table (api.handler) calls
//     apply_entity_table_standards inline, so it is unaffected;
//   - test_entity_standards.sql invokes the sweep itself, so it passes either
//     way.
//
// Delete the call and only a user's own table loses its columns — silently at
// deploy time, then loudly at runtime when the soft-delete views query
// deleted_at.
func TestAdvancedDeploySQLWiresTheEntityStandardsSweep(t *testing.T) {
	read := func(name string) (raw, code string) {
		body, err := os.ReadFile("../../shared/templates/advanced/" + name)
		if err != nil {
			t.Fatalf("read advanced %s: %v", name, err)
		}
		// Comments and literals masked: deploy.sql's header names files and
		// calls that are not statements.
		return string(body), preprocessor.NewCommentStripper().RedactForMacros(string(body))
	}

	const sweep = "pg_temp.apply_entity_standards_all()"
	if _, finalize := read("deploy/finalize.sql"); !strings.Contains(finalize, sweep) {
		t.Fatalf("deploy/finalize.sql never calls %s — a user table declaring "+
			"object_id core.entity_id would not get created_at/deleted_at", sweep)
	}

	// Ordering is load-bearing in both directions: deploy.sql runs finalize.sql
	// after the plan loop and before the test gate.
	raw, code := read("deploy.sql")
	const runFinalize = "pg_temp.run_file('./deploy/finalize.sql')"
	finalizeAt := strings.Index(raw, runFinalize)
	if finalizeAt == -1 || !strings.HasPrefix(code[finalizeAt:], "pg_temp.run_file(") {
		t.Fatal("deploy.sql never runs deploy/finalize.sql")
	}
	loopAt := strings.Index(raw, "EXECUTE f.content")
	if loopAt == -1 {
		t.Fatal("deploy.sql has no plan loop executing f.content")
	}
	if finalizeAt < loopAt {
		t.Error("finalize.sql runs before the plan loop, so the sweep cannot see tables the " +
			"deployment just created")
	}

	testsAt := strings.Index(code, "CALL pgmi_test(")
	if testsAt == -1 {
		t.Fatal("deploy.sql never calls pgmi_test()")
	}
	if finalizeAt > testsAt {
		t.Error("the sweep runs after the test gate, so tests see un-swept tables and a " +
			"conformance failure would abort the deploy only after the tests claimed it was fine")
	}
}
