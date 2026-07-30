package checksum

import "testing"

// The normalized checksum is what users track to decide whether a migration
// re-runs, so two different files sharing one is not a cosmetic bug: the second
// file silently never executes.
//
// Two collisions existed. removeComments had no state for quoted identifiers,
// and did not know that a backslash escapes inside E'...', so in both cases a
// -- inside a literal or identifier started a "comment" that ate the rest of
// the line — including the part that differed.
func TestNormalizationDistinguishesStatementsThatDiffer(t *testing.T) {
	c := New()

	tests := []struct {
		name string
		a, b string
	}{
		{
			name: "escaped quote in an E-string does not start a comment",
			a:    `SELECT E'a\'-- keep this' , 1;`,
			b:    `SELECT E'a\'-- keep this' , 2;`,
		},
		{
			name: "-- inside a quoted identifier is not a comment",
			a:    `SELECT "a--b" , 1 FROM t;`,
			b:    `SELECT "a--b" , 2 FROM t;`,
		},
		{
			name: "block comment inside a quoted identifier is part of the name",
			a:    `SELECT "a/*x*/b" , 1 FROM t;`,
			b:    `SELECT "a/*x*/b" , 2 FROM t;`,
		},
		{
			name: "doubled quote inside a quoted identifier",
			a:    `SELECT "a""--b" , 1 FROM t;`,
			b:    `SELECT "a""--b" , 2 FROM t;`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if c.CalculateNormalized([]byte(tt.a)) == c.CalculateNormalized([]byte(tt.b)) {
				t.Errorf("different statements share a normalized checksum:\n  %s\n  %s\n"+
					"normalized to %q and %q",
					tt.a, tt.b, c.removeComments(tt.a), c.removeComments(tt.b))
			}
		})
	}
}

// The whole point of the normalized checksum: comment and layout edits are free.
// Guarding it here too, because the fix above added states to the same walk.
func TestNormalizationStillIgnoresCommentsAndLayout(t *testing.T) {
	c := New()

	same := [][2]string{
		{"SELECT 1;", "SELECT   1; -- trailing note"},
		{"SELECT 1;", "/* banner */\nSELECT\n1;"},
		{"SELECT 1;", "/* outer /* nested */ still outer */ SELECT 1;"},
		{`SELECT 'a';`, "SELECT 'a'; /* note */"},
		{`SELECT E'a\'b';`, "SELECT E'a\\'b';   -- note"},
		{"DO $$ BEGIN END $$;", "DO $$ BEGIN END $$; -- note"},
	}

	for _, pair := range same {
		if c.CalculateNormalized([]byte(pair[0])) != c.CalculateNormalized([]byte(pair[1])) {
			t.Errorf("a comment or layout edit changed the checksum:\n  %q\n  %q\n"+
				"normalized to %q and %q",
				pair[0], pair[1], c.removeComments(pair[0]), c.removeComments(pair[1]))
		}
	}
}

// A comment must normalize to a separator, not vanish: deleting it outright
// would make a/*x*/b indistinguishable from the different statement ab.
func TestCommentNormalizesToASeparator(t *testing.T) {
	c := New()
	if c.CalculateNormalized([]byte("SELECT a/*x*/b;")) == c.CalculateNormalized([]byte("SELECT ab;")) {
		t.Error("a comment between two tokens was deleted rather than replaced with a separator")
	}
}
