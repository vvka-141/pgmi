package scaffold_test

import (
	"os"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/preprocessor"
)

// SplitExecutionUnits decides where a deploy stops being atomic. It is unit
// tested on synthetic input and was never run over either shipped deploy.sql —
// the two scripts every project starts from, and the only ones where the
// dollar-quote tracking has to survive hundreds of lines of real PL/pgSQL.
//
// The failure mode is severe and silent. advanced/deploy.sql closes a function
// body with a bare `END;` at column 0 (deployment_setting, ~line 147) long
// before its real COMMIT. Read as a top-level transaction terminator, the split
// moves there and everything after it — roles, extensions, schema creation, the
// plan loop, the test gate — runs in psql mode with per-statement autocommit.
// The deploy still succeeds. Atomicity is simply gone, and nobody finds out
// until a mid-deploy failure leaves the database half-migrated.
//
// Both templates also write `ROLLBACK TO SAVEPOINT _tests;` immediately before
// COMMIT, so this exercises the other trap in the same pass: ROLLBACK is in the
// terminator vocabulary, and ROLLBACK TO SAVEPOINT must not be.
func TestShippedTemplatesSplitAtTheirDocumentedCommit(t *testing.T) {
	for _, tc := range []struct {
		template string
		// A fragment from inside the dollar-quoted body whose bare END; could
		// be mistaken for a terminator. Must land in the atomic head.
		beforeFalseTerminator string
		// A fragment of each tail unit, in order.
		tail []string
	}{
		{"basic", "SAVEPOINT _tests", []string{"RAISE NOTICE"}},
		{"advanced", "Required deployment parameter", []string{
			"Deployment lock released", "NOT i.indisvalid", "RAISE NOTICE $ascii$"}},
	} {
		t.Run(tc.template, func(t *testing.T) {
			body, err := os.ReadFile("templates/" + tc.template + "/deploy.sql")
			if err != nil {
				t.Fatalf("read deploy.sql: %v", err)
			}

			units := preprocessor.SplitExecutionUnits(string(body))
			if len(units) != 1+len(tc.tail) {
				t.Fatalf("splits into %d execution unit(s), want %d (atomic head + %d tail statements); "+
					"more means a mid-file terminator was found, 1 means the real COMMIT was missed",
					len(units), 1+len(tc.tail), len(tc.tail))
			}

			head := strings.TrimSpace(units[0])

			if !strings.HasSuffix(head, "COMMIT;") {
				t.Errorf("the atomic head does not end at COMMIT; it ends with %q",
					lastRunes(head, 60))
			}
			if !strings.Contains(head, tc.beforeFalseTerminator) {
				t.Errorf("%q is missing from the atomic head — the split happened at a bare END; "+
					"inside a dollar-quoted body instead of at the real COMMIT",
					tc.beforeFalseTerminator)
			}
			if !strings.Contains(head, "ROLLBACK TO SAVEPOINT") {
				t.Error("ROLLBACK TO SAVEPOINT is not in the atomic head — it was treated as a " +
					"transaction terminator, so the split moved one statement early")
			}

			// The product claim is test-gated deployment: a failing test must
			// roll back the deploy. That only holds while the gate runs inside
			// the atomic head.
			if !strings.Contains(head, "pgmi_test") {
				t.Error("the test gate is not in the atomic head — a failing test would no longer " +
					"roll the deployment back")
			}

			// Anything a user appends lands in the tail and autocommits, which
			// is what the docs warn about.
			for i, want := range tc.tail {
				unit := units[1+i]
				if !strings.Contains(unit, want) {
					t.Errorf("tail statement %d is not the expected one (%q), it is %q", i+1, want, firstRunes(strings.TrimSpace(unit), 60))
				}
				if strings.Contains(unit, "COMMIT;") {
					t.Error("a second COMMIT is in the tail — the head ended at the wrong terminator")
				}
			}
		})
	}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return "..." + string(r[len(r)-n:])
	}
	return s
}
