package services

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/ai"
)

// TestStepTypeCompleteness pins pgmi_test_plan's step_type vocabulary from both
// ends. A deploy.sql that branches on step_type — the reason the values are in
// the contract at all — silently skips work for a value it was never told
// about, and a value that no longer exists sends an author writing a branch
// that never runs.
//
// The existing drift test only asserts each declared value appears as a literal
// somewhere in the SQL, which neither direction covers.
func TestStepTypeCompleteness(t *testing.T) {
	declared := ai.GetContract().StepTypes
	if len(declared) == 0 {
		t.Fatal("the contract declares no step types")
	}

	t.Run("a real plan yields exactly the declared values", func(t *testing.T) {
		connString := requireTestDB(t)
		testDB := "pgmi_itest_step_types"
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

		prepareSessionTables(t, ctx, conn)

		// A nested tree with fixtures at both levels: the shape that produces
		// every step kind, teardown included.
		seed := []string{
			`INSERT INTO pg_temp._pgmi_test_directory (path, parent_path, depth) VALUES
			   ('./__test__/', NULL, 1),
			   ('./__test__/nested/', './__test__/', 2)`,
			`INSERT INTO pg_temp._pgmi_test_source (path, directory, filename, content, is_fixture) VALUES
			   ('./__test__/_setup.sql',           './__test__/',       '_setup.sql', 'SELECT 1;', true),
			   ('./__test__/test_a.sql',           './__test__/',       'test_a.sql', 'SELECT 1;', false),
			   ('./__test__/nested/_setup.sql',    './__test__/nested/','_setup.sql', 'SELECT 1;', true),
			   ('./__test__/nested/test_b.sql',    './__test__/nested/','test_b.sql', 'SELECT 1;', false)`,
		}
		for _, stmt := range seed {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}

		rows, err := conn.Query(ctx, `SELECT DISTINCT step_type FROM pg_temp.pgmi_test_plan() ORDER BY 1`)
		if err != nil {
			t.Fatalf("pgmi_test_plan: %v", err)
		}
		defer rows.Close()

		var observed []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatalf("scan: %v", err)
			}
			observed = append(observed, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate: %v", err)
		}

		want := slices.Clone(declared)
		slices.Sort(want)
		if !slices.Equal(observed, want) {
			t.Errorf("plan step types differ:\n  observed %s\n  contract %s",
				strings.Join(observed, ", "), strings.Join(want, ", "))
		}
	})

	// The live half cannot see a value no fixture happens to produce, so the
	// literals in the function body are checked too.
	t.Run("the function emits no undeclared value", func(t *testing.T) {
		body, err := os.ReadFile("../params/schema.sql")
		if err != nil {
			t.Fatalf("read schema.sql: %v", err)
		}

		start := strings.Index(string(body), "CREATE OR REPLACE FUNCTION pg_temp.pgmi_test_plan")
		if start == -1 {
			t.Fatal("pgmi_test_plan not found in schema.sql")
		}
		fn, _, _ := strings.Cut(string(body)[start:], "\n$$;")

		// step_type is produced as a literal in each UNION arm: 'fixture' AS
		// step_type, then bare literals in the arms that follow.
		emitted := map[string]bool{}
		for _, m := range regexp.MustCompile(`'([a-z]+)'(?:\s+AS\s+step_type)?,`).FindAllStringSubmatch(fn, -1) {
			if slices.Contains(declared, m[1]) {
				emitted[m[1]] = true
			}
		}
		for _, d := range declared {
			if !emitted[d] {
				t.Errorf("contract declares step type %q, but pgmi_test_plan never emits it", d)
			}
		}
	})
}
