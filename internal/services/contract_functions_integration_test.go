package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/vvka-141/pgmi/internal/contract"
)

// TestContractFunctionSignaturesMatchLiveSession checks ai.GetContract()'s
// function signatures against the session PostgreSQL actually creates.
//
// `pgmi ai contract` exists so an agent does not have to guess identifiers, so
// a wrong parameter name there is worse than no entry: named notation --
// pgmi_test_plan(p_pattern => 'auth') -- fails with "function does not exist",
// and the agent has no reason to doubt the contract.
//
// The static guard in internal/ai matches the declared default as a substring
// of schema.sql, which cannot catch this: "pattern text DEFAULT NULL" is a
// substring of "p_pattern TEXT DEFAULT NULL".
func TestContractFunctionSignaturesMatchLiveSession(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_contract_functions"
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
	if _, err := contract.Apply(ctx, conn, ""); err != nil {
		t.Fatalf("apply contract: %v", err)
	}

	for _, fn := range ai.GetContract().Functions {
		t.Run(fn.Name, func(t *testing.T) {
			var args string
			err := conn.QueryRow(ctx, `
				SELECT pg_get_function_arguments(p.oid)
				FROM pg_proc p
				JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = pg_my_temp_schema()::regnamespace::text
				  AND p.proname = $1`, fn.Name).Scan(&args)
			if err != nil {
				t.Fatalf("%s is in the contract but not in the session: %v", fn.Name, err)
			}

			// Compare parameter names only. Types and defaults are rendered
			// differently by pg_get_function_arguments than the contract writes
			// them, but a name that does not exist is the failure that matters.
			wantNames := paramNames(fn.Args)
			gotNames := paramNames(strings.Split(args, ", "))

			if fmt.Sprint(wantNames) != fmt.Sprint(gotNames) {
				t.Errorf("parameter names differ:\n  contract %v\n  session  %v\n  session signature: %s",
					wantNames, gotNames, args)
			}

			var result string
			if err := conn.QueryRow(ctx, `
				SELECT pg_get_function_result(p.oid)
				FROM pg_proc p
				JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = pg_my_temp_schema()::regnamespace::text
				  AND p.proname = $1`, fn.Name).Scan(&result); err != nil {
				t.Fatalf("result of %s: %v", fn.Name, err)
			}

			// A TABLE-returning function is destructured by column name, so the
			// contract lists those; a scalar one is named by its type.
			want := strings.Join(fn.Returns, ", ")
			got := result
			if cols, ok := strings.CutPrefix(result, "TABLE("); ok {
				got = strings.Join(paramNames(strings.Split(strings.TrimSuffix(cols, ")"), ", ")), ", ")
			}
			if want != got {
				t.Errorf("return shape differs:\n  contract %q\n  session  %q (%s)", want, got, result)
			}
		})
	}
}

// The contract is what an agent reads instead of guessing, so a session object
// it does not mention is invisible. Anything deliberately withheld belongs
// here with the reason, not in the gap between the two.
var undeclaredSessionFunctions = map[string]string{
	"pgmi_run_test_source": "internal helper the pgmi_test() expansion calls; deploy.sql never does",
}

func TestEverySessionFunctionIsDeclaredOrExempt(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_session_functions"
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
	if _, err := contract.Apply(ctx, conn, ""); err != nil {
		t.Fatalf("apply contract: %v", err)
	}

	declared := map[string]bool{}
	for _, fn := range ai.GetContract().Functions {
		declared[fn.Name] = true
	}

	rows, err := conn.Query(ctx, `
		SELECT p.proname
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = pg_my_temp_schema()::regnamespace::text
		ORDER BY p.proname`)
	if err != nil {
		t.Fatalf("list session functions: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if declared[name] {
			continue
		}
		if why, ok := undeclaredSessionFunctions[name]; ok {
			t.Logf("%s is undeclared on purpose: %s", name, why)
			continue
		}
		t.Errorf("the session creates %s, which the contract does not mention — "+
			"declare it, or add it to undeclaredSessionFunctions with a reason", name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
}

// The composite type a custom test callback receives. Its field names and
// types are the callback's whole interface.
func TestContractTestEventTypeMatchesLiveSession(t *testing.T) {
	connString := requireTestDB(t)
	testDB := "pgmi_itest_test_event_type"
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

	for _, ct := range ai.GetContract().Types {
		t.Run(ct.Name, func(t *testing.T) {
			rows, err := conn.Query(ctx, `
				SELECT a.attname, format_type(a.atttypid, a.atttypmod)
				FROM pg_type t
				JOIN pg_class c ON c.oid = t.typrelid
				JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
				JOIN pg_namespace n ON n.oid = t.typnamespace
				WHERE n.nspname = pg_my_temp_schema()::regnamespace::text AND t.typname = $1
				ORDER BY a.attnum`, ct.Name)
			if err != nil {
				t.Fatalf("describe %s: %v", ct.Name, err)
			}
			defer rows.Close()

			var got []string
			for rows.Next() {
				var name, typ string
				if err := rows.Scan(&name, &typ); err != nil {
					t.Fatalf("scan: %v", err)
				}
				got = append(got, name+" "+typ)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate: %v", err)
			}
			if len(got) == 0 {
				t.Fatalf("%s is in the contract but the session creates no such type", ct.Name)
			}

			var want []string
			for _, f := range ct.Fields {
				want = append(want, f.Name+" "+f.Type)
			}
			if strings.Join(want, ", ") != strings.Join(got, ", ") {
				t.Errorf("fields differ (order matters — callbacks destructure positionally):\n"+
					"  contract %v\n  session  %v", want, got)
			}
		})
	}
}

// paramNames takes the leading identifier of each "name type [DEFAULT ...]"
// fragment, skipping any leading INOUT-style mode keyword.
func paramNames(args []string) []string {
	names := make([]string, 0, len(args))
	for _, a := range args {
		fields := strings.Fields(a)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "IN", "OUT", "INOUT", "VARIADIC":
			fields = fields[1:]
		}
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names
}
