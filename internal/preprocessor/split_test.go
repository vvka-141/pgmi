package preprocessor

import (
	"context"
	"strings"
	"testing"
)

func TestSplitExecutionUnits(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string // expected units with padding stripped; nil means single unit == input
	}{
		{
			name: "no top-level terminator is a single unit",
			sql:  "CREATE TABLE t (id int);\nINSERT INTO t VALUES (1);\n",
		},
		{
			name: "BEGIN without COMMIT is a single unit",
			sql:  "BEGIN;\nCREATE TABLE t (id int);\n",
		},
		{
			name: "trailing COMMIT with nothing after is a single unit",
			sql:  "BEGIN;\nCREATE TABLE t (id int);\nCOMMIT;\n",
		},
		{
			name: "trailing COMMIT followed only by comments is a single unit",
			sql:  "BEGIN;\nSELECT 1;\nCOMMIT;\n-- done\n/* all done */\n",
		},
		{
			name: "statements after first COMMIT become per-statement units",
			sql:  "BEGIN;\nCREATE TABLE t (id int);\nCOMMIT;\nCREATE INDEX CONCURRENTLY i ON t (id);\nANALYZE t;\n",
			want: []string{
				"BEGIN;\nCREATE TABLE t (id int);\nCOMMIT;",
				"CREATE INDEX CONCURRENTLY i ON t (id);",
				"ANALYZE t;",
			},
		},
		{
			name: "first terminator anchors, not the last",
			sql:  "ALTER TABLE t ADD COLUMN c int;\nCOMMIT;\nCREATE INDEX CONCURRENTLY i1 ON t (c);\nBEGIN;\nUPDATE t SET c = 1;\nCOMMIT;\nCREATE INDEX CONCURRENTLY i2 ON t (c);\n",
			want: []string{
				"ALTER TABLE t ADD COLUMN c int;\nCOMMIT;",
				"CREATE INDEX CONCURRENTLY i1 ON t (c);",
				"BEGIN;",
				"UPDATE t SET c = 1;",
				"COMMIT;",
				"CREATE INDEX CONCURRENTLY i2 ON t (c);",
			},
		},
		{
			name: "END is a COMMIT synonym and anchors",
			sql:  "SELECT 1;\nEND;\nSELECT 2;\n",
			want: []string{"SELECT 1;\nEND;", "SELECT 2;"},
		},
		{
			name: "END TRANSACTION anchors",
			sql:  "SELECT 1;\nEND TRANSACTION;\nSELECT 2;\n",
			want: []string{"SELECT 1;\nEND TRANSACTION;", "SELECT 2;"},
		},
		{
			name: "COMMIT WORK anchors",
			sql:  "SELECT 1;\nCOMMIT WORK;\nSELECT 2;\n",
			want: []string{"SELECT 1;\nCOMMIT WORK;", "SELECT 2;"},
		},
		{
			name: "COMMIT AND CHAIN anchors",
			sql:  "SELECT 1;\nCOMMIT AND CHAIN;\nSELECT 2;\nCOMMIT;\n",
			want: []string{"SELECT 1;\nCOMMIT AND CHAIN;", "SELECT 2;", "COMMIT;"},
		},
		{
			name: "top-level ROLLBACK anchors",
			sql:  "SELECT 1;\nROLLBACK;\nSELECT 2;\n",
			want: []string{"SELECT 1;\nROLLBACK;", "SELECT 2;"},
		},
		{
			name: "ROLLBACK AND CHAIN anchors",
			sql:  "SELECT 1;\nROLLBACK AND CHAIN;\nSELECT 2;\n",
			want: []string{"SELECT 1;\nROLLBACK AND CHAIN;", "SELECT 2;"},
		},
		{
			name: "ROLLBACK TO SAVEPOINT does not anchor",
			sql:  "BEGIN;\nSAVEPOINT s;\nROLLBACK TO SAVEPOINT s;\nCOMMIT;\n",
		},
		{
			name: "ROLLBACK TRANSACTION TO SAVEPOINT does not anchor",
			sql:  "BEGIN;\nSAVEPOINT s;\nROLLBACK TRANSACTION TO SAVEPOINT s;\nCREATE TABLE t (id int);\nCOMMIT;\n",
		},
		{
			name: "ROLLBACK WORK TO does not anchor",
			sql:  "BEGIN;\nSAVEPOINT s;\nROLLBACK WORK TO s;\nCREATE TABLE t (id int);\nCOMMIT;\n",
		},
		{
			name: "ABORT anchors like ROLLBACK",
			sql:  "BEGIN;\nSELECT 1;\nABORT;\nCREATE INDEX CONCURRENTLY i ON t (id);\n",
			want: []string{"BEGIN;\nSELECT 1;\nABORT;", "CREATE INDEX CONCURRENTLY i ON t (id);"},
		},
		{
			name: "ABORT TRANSACTION TO SAVEPOINT does not anchor",
			sql:  "BEGIN;\nSAVEPOINT s;\nABORT TRANSACTION TO SAVEPOINT s;\nCOMMIT;\n",
		},
		{
			name: "invalid UTF-8 falls back to a single unit instead of panicking",
			sql:  "BEGIN;\n-- caf\xe9 comment\nSELECT 1;\nCOMMIT;\nSELECT 2;\n",
		},
		{
			name: "COMMIT PREPARED does not anchor",
			sql:  "COMMIT PREPARED 'gid';\nSELECT 1;\n",
		},
		{
			name: "terminator as the very first statement",
			sql:  "COMMIT;\nSELECT 1;\n",
			want: []string{"COMMIT;", "SELECT 1;"},
		},
		{
			name: "COMMIT inside dollar-quoted body does not anchor",
			sql:  "DO $$\nBEGIN\n    EXECUTE 'COMMIT;';\nEND $$;\nSELECT 'COMMIT; SELECT 2;';\n",
		},
		{
			name: "COMMIT inside single-quoted string does not anchor",
			sql:  "SELECT 'COMMIT;';\nSELECT 1;\n",
		},
		{
			name: "COMMIT inside comments does not anchor",
			sql:  "-- COMMIT;\n/* COMMIT; */\nSELECT 1;\n",
		},
		{
			name: "semicolons inside parentheses do not split statements",
			sql:  "CREATE RULE r AS ON DELETE TO t DO (SELECT 1; SELECT 2);\nCOMMIT;\nSELECT 3;\n",
			want: []string{
				"CREATE RULE r AS ON DELETE TO t DO (SELECT 1; SELECT 2);\nCOMMIT;",
				"SELECT 3;",
			},
		},
		{
			name: "tail statement without trailing semicolon",
			sql:  "SELECT 1;\nCOMMIT;\nCREATE INDEX CONCURRENTLY i ON t (id)",
			want: []string{"SELECT 1;\nCOMMIT;", "CREATE INDEX CONCURRENTLY i ON t (id)"},
		},
		{
			name: "case-insensitive terminators",
			sql:  "SELECT 1;\ncommit;\nSELECT 2;\n",
			want: []string{"SELECT 1;\ncommit;", "SELECT 2;"},
		},
		{
			name: "BEGIN ATOMIC body END does not anchor",
			sql:  "CREATE FUNCTION f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; END;\nCREATE TABLE t (id int);\n",
		},
		{
			name: "multi-statement BEGIN ATOMIC body does not anchor",
			sql:  "CREATE FUNCTION f() RETURNS int LANGUAGE SQL\nBEGIN ATOMIC\n    SELECT 1;\n    SELECT 2;\nEND;\nCREATE TABLE t (id int);\n",
		},
		{
			name: "BEGIN ATOMIC in the tail stays one unit",
			sql:  "SELECT 1;\nCOMMIT;\nCREATE FUNCTION g() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; END;\nANALYZE t;\n",
			want: []string{
				"SELECT 1;\nCOMMIT;",
				"CREATE FUNCTION g() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; END;",
				"ANALYZE t;",
			},
		},
		{
			name: "BEGIN ATOMIC body with parentheses stays one unit",
			sql:  "SELECT 1;\nCOMMIT;\nCREATE FUNCTION g() RETURNS void LANGUAGE SQL BEGIN ATOMIC INSERT INTO t VALUES (1); INSERT INTO t VALUES (2); END;\n",
			want: []string{
				"SELECT 1;\nCOMMIT;",
				"CREATE FUNCTION g() RETURNS void LANGUAGE SQL BEGIN ATOMIC INSERT INTO t VALUES (1); INSERT INTO t VALUES (2); END;",
			},
		},
		{
			name: "CASE END inside a BEGIN ATOMIC body does not close it",
			sql:  "SELECT 1;\nCOMMIT;\nCREATE FUNCTION g() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT CASE WHEN true THEN 1 ELSE 2 END; END;\nANALYZE t;\n",
			want: []string{
				"SELECT 1;\nCOMMIT;",
				"CREATE FUNCTION g() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT CASE WHEN true THEN 1 ELSE 2 END; END;",
				"ANALYZE t;",
			},
		},
		{
			name: "empty BEGIN ATOMIC body closes in its own statement",
			sql:  "CREATE PROCEDURE p() LANGUAGE SQL BEGIN ATOMIC END;\nSELECT 1;\nCOMMIT;\nSELECT 2;\n",
			want: []string{
				"CREATE PROCEDURE p() LANGUAGE SQL BEGIN ATOMIC END;\nSELECT 1;\nCOMMIT;",
				"SELECT 2;",
			},
		},
		{
			name: "BEGIN and ATOMIC as separate identifiers do not open a body",
			sql:  "SELECT begin, atomic FROM t;\nCOMMIT;\nSELECT 2;\n",
			want: []string{"SELECT begin, atomic FROM t;\nCOMMIT;", "SELECT 2;"},
		},
		{
			name: "BEGIN ATOMIC inside a dollar-quoted body is invisible",
			sql:  "DO $$\nBEGIN\n    EXECUTE 'CREATE FUNCTION f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; END';\nEND $$;\nCOMMIT;\nSELECT 2;\n",
			want: []string{
				"DO $$\nBEGIN\n    EXECUTE 'CREATE FUNCTION f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; END';\nEND $$;\nCOMMIT;",
				"SELECT 2;",
			},
		},
		{
			name: "semicolon and COMMIT inside a quoted identifier do not anchor",
			sql:  "SELECT 1 AS \"note; COMMIT\";\nSELECT 2;\n",
		},
		{
			name: "doubled quote inside a quoted identifier does not end it",
			sql:  "SELECT 1 AS \"a\"\"; COMMIT; b\";\nSELECT 2;\n",
		},
		{
			name: "apostrophe inside a quoted identifier does not open a literal",
			sql:  "SELECT n AS \"Client's Name\" FROM t;\nCOMMIT;\nSELECT 2;\n",
			want: []string{"SELECT n AS \"Client's Name\" FROM t;\nCOMMIT;", "SELECT 2;"},
		},
		{
			name: "double dash inside a quoted identifier does not start a comment",
			sql:  "COMMENT ON COLUMN t.\"a--b\" IS 'x';\nSELECT 1;\nCOMMIT;\nSELECT 2;\n",
			want: []string{"COMMENT ON COLUMN t.\"a--b\" IS 'x';\nSELECT 1;\nCOMMIT;", "SELECT 2;"},
		},
		{
			name: "block comment open inside a quoted identifier is inert",
			sql:  "SELECT 1 AS \"a/*b\";\nCOMMIT;\nSELECT 2;\n",
			want: []string{"SELECT 1 AS \"a/*b\";\nCOMMIT;", "SELECT 2;"},
		},
		{
			name: "dollar tag inside a quoted identifier does not open a dollar quote",
			sql:  "SELECT 1 AS \"a$tag$b\";\nCOMMIT;\nSELECT 2;\n",
			want: []string{"SELECT 1 AS \"a$tag$b\";\nCOMMIT;", "SELECT 2;"},
		},
		{
			name: "backslash-escaped quote in an E-string does not end the literal",
			sql:  "UPDATE t SET note = E'legacy\\'; COMMIT; keep' WHERE id = 1;\nSELECT 1;\n",
		},
		{
			name: "doubled backslash in an E-string leaves the quote closing",
			sql:  "SELECT E'a\\\\';\nCOMMIT;\nSELECT 2;\n",
			want: []string{"SELECT E'a\\\\';\nCOMMIT;", "SELECT 2;"},
		},
		{
			name: "lowercase e prefix is an escape string too",
			sql:  "SELECT e'a\\'; COMMIT; b';\nSELECT 1;\n",
		},
		{
			name: "backslash in an ordinary literal is not an escape",
			sql:  "SELECT 'a\\';\nCOMMIT;\nSELECT 2;\n",
			want: []string{"SELECT 'a\\';\nCOMMIT;", "SELECT 2;"},
		},
		{
			// date'...' is a type-prefixed literal, not an escape string: the
			// e belongs to the type name, so the backslash stays literal.
			name: "identifier ending in e does not make the next literal an escape string",
			sql:  "SELECT date'a\\';\nCOMMIT;\nSELECT 2;\n",
			want: []string{"SELECT date'a\\';\nCOMMIT;", "SELECT 2;"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitExecutionUnits(tt.sql)

			if tt.want == nil {
				if len(got) != 1 || got[0] != tt.sql {
					t.Fatalf("expected a single unchanged unit, got %d units: %q", len(got), got)
				}
				return
			}

			if len(got) != len(tt.want) {
				t.Fatalf("expected %d units, got %d: %q", len(tt.want), len(got), got)
			}
			for i, unit := range got {
				if trimmed := strings.TrimSpace(unit); trimmed != tt.want[i] {
					t.Errorf("unit %d = %q, want %q", i, trimmed, tt.want[i])
				}
			}
		})
	}
}

func TestSplitExecutionUnits_CRLF(t *testing.T) {
	lfTests := []struct {
		name string
		sql  string
		want int
	}{
		{"head+tail with LF", "BEGIN;\nSELECT 1;\nCOMMIT;\nSELECT 2;\n", 2},
		{"single unit with LF", "BEGIN;\nSELECT 1;\nCOMMIT;\n", 1},
		{"three tail units", "COMMIT;\nSELECT 1;\nSELECT 2;\n", 3},
	}

	for _, tt := range lfTests {
		t.Run(tt.name, func(t *testing.T) {
			crlf := strings.ReplaceAll(tt.sql, "\n", "\r\n")
			lfUnits := SplitExecutionUnits(tt.sql)
			crlfUnits := SplitExecutionUnits(crlf)
			if len(crlfUnits) != len(lfUnits) {
				t.Errorf("CRLF produced %d units, LF produced %d", len(crlfUnits), len(lfUnits))
			}
			if len(crlfUnits) != tt.want {
				t.Errorf("expected %d units, got %d", tt.want, len(crlfUnits))
			}
		})
	}
}

// Tail units must be padded so a PostgreSQL error position resolves to the same
// line and column it would in the full script. That, not byte offset, is the
// contract: LocateError counts newlines for the line and characters since the
// last one for the column, so the padding only has to reproduce those two.
func TestSplitExecutionUnitsPreservesPositions(t *testing.T) {
	lineCol := func(s string, idx int) (line, col int) {
		line = 1 + strings.Count(s[:idx], "\n")
		return line, idx - strings.LastIndexByte(s[:idx], '\n')
	}

	sql := "BEGIN;\nSELECT 1;\nCOMMIT;\nSELECT 'a';\nSELECT 'b';\n"
	units := SplitExecutionUnits(sql)
	if len(units) != 3 {
		t.Fatalf("expected 3 units, got %d: %q", len(units), units)
	}

	for i, unit := range units[1:] {
		stmt := []string{"SELECT 'a';", "SELECT 'b';"}[i]
		wantLine, wantCol := lineCol(sql, strings.Index(sql, stmt))
		gotLine, gotCol := lineCol(unit, strings.Index(unit, "SELECT"))

		if gotLine != wantLine || gotCol != wantCol {
			t.Errorf("unit %d: statement starts at %d:%d, want %d:%d (padding must preserve line and column)",
				i+1, gotLine, gotCol, wantLine, wantCol)
		}
	}
}

// A CALL pgmi_test() macro expanded in the head stays inside one unit: the
// generated SAVEPOINT/ROLLBACK TO SAVEPOINT statements are part of the head
// message, so they execute within the caller's explicit transaction.
func TestExpandedMacroDoesNotMoveTheHeadAnchor(t *testing.T) {
	mockExpansion := strings.Join([]string{
		"SAVEPOINT __pgmi_d1__;",
		"SELECT pg_temp.pgmi_test_default_callback(ROW('test_start')::pg_temp.pgmi_test_event);",
		"DO $__pgmi__$ BEGIN EXECUTE (SELECT content FROM pg_temp._pgmi_test_source WHERE path = './__test__/test_basic.sql'); END $__pgmi__$;",
		"ROLLBACK TO SAVEPOINT __pgmi_d1__;",
		"RELEASE SAVEPOINT __pgmi_d1__;",
	}, "\n")

	p := newTestPipeline(mockTestGenerate(map[string]string{
		"": mockExpansion,
	}))
	result, err := p.Process(context.Background(), nil, "BEGIN;\nCALL pgmi_test();\nCOMMIT;\nSELECT 'tail';\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	units := SplitExecutionUnits(result.ExpandedSQL)
	if len(units) != 2 {
		t.Fatalf("expected head + 1 tail unit, got %d: %q", len(units), units)
	}

	head := strings.TrimSpace(units[0])
	if !strings.HasSuffix(head, "COMMIT;") {
		t.Errorf("head should end at the user's COMMIT, got suffix: %q", head[max(0, len(head)-30):])
	}
	if !strings.Contains(head, "SAVEPOINT __pgmi_d1__") {
		t.Error("SAVEPOINT from the macro expansion must be inside the head unit")
	}
	if !strings.Contains(head, "ROLLBACK TO SAVEPOINT __pgmi_d1__") {
		t.Error("ROLLBACK TO SAVEPOINT from the macro expansion must be inside the head unit")
	}
}

// A macro expanded in the tail produces savepoint statements as individual
// autocommitted messages — PostgreSQL refuses SAVEPOINT outside a transaction
// block with 25P01. This test documents the structural consequence: the
// expansion is correct SQL, but the split puts each statement on its own.
func TestExpandedMacroInTailProducesBrokenSavepoints(t *testing.T) {
	mockExpansion := strings.Join([]string{
		"SAVEPOINT __pgmi_d1__;",
		"DO $__pgmi__$ BEGIN EXECUTE 'SELECT 1'; END $__pgmi__$;",
		"ROLLBACK TO SAVEPOINT __pgmi_d1__;",
		"RELEASE SAVEPOINT __pgmi_d1__;",
	}, "\n")

	p := newTestPipeline(mockTestGenerate(map[string]string{
		"": mockExpansion,
	}))
	result, err := p.Process(context.Background(), nil, "BEGIN;\nSELECT 1;\nCOMMIT;\nCALL pgmi_test();\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	units := SplitExecutionUnits(result.ExpandedSQL)
	// The tail contains 4 statements from the expansion, each becomes its own unit.
	// Head(1) + 4 tail statements = 5 units.
	if len(units) < 3 {
		t.Fatalf("expected at least 3 execution units (head + tail statements), got %d", len(units))
	}

	head := strings.TrimSpace(units[0])
	if !strings.HasSuffix(head, "COMMIT;") {
		t.Fatalf("head should end at COMMIT, got: %q", head)
	}

	savepointInTail := false
	for _, unit := range units[1:] {
		trimmed := strings.TrimSpace(unit)
		if strings.HasPrefix(trimmed, "SAVEPOINT ") {
			savepointInTail = true
			break
		}
	}
	if !savepointInTail {
		t.Error("expected at least one bare SAVEPOINT in the tail units (this would fail with 25P01 at runtime)")
	}
}

// The scaffolded templates end with COMMIT followed by a single banner DO
// block; the split must produce exactly head + one autocommit unit for them.
func TestSplitExecutionUnitsTemplateShape(t *testing.T) {
	sql := "BEGIN;\nSELECT pg_temp.deploy();\nSAVEPOINT _tests;\nROLLBACK TO SAVEPOINT _tests;\nCOMMIT;\nDO $$\nBEGIN\n    RAISE NOTICE 'done';\nEND $$;\n"
	units := SplitExecutionUnits(sql)
	if len(units) != 2 {
		t.Fatalf("expected head + banner unit, got %d: %q", len(units), units)
	}
	if !strings.HasSuffix(strings.TrimSpace(units[0]), "COMMIT;") {
		t.Errorf("head should end at the first top-level COMMIT, got %q", units[0])
	}
	if !strings.Contains(units[1], "RAISE NOTICE") {
		t.Errorf("banner DO block should be the tail unit, got %q", units[1])
	}
}
