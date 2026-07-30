package checksum

import (
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/preprocessor"
)

// Two SQL comment scanners survive in this repo — this one and the
// preprocessor's — because their output contracts differ: a comment becomes a
// space here so a/*x*/b cannot collide with ab, and disappears there. What must
// never differ is which bytes they consider a comment. A third scanner in
// internal/metadata disagreed with both until it was replaced by a call to
// preprocessor.BlockComments, and this one shipped two collisions.
//
// preprocessor.BlockComments is the authority. For inputs with no -- comments,
// removeComments must blank exactly those spans and touch nothing else.
func TestCommentDetectionAgreesWithPreprocessor(t *testing.T) {
	c := New()
	spansOf := preprocessor.NewCommentStripper().BlockComments

	inputs := []string{
		`SELECT 1;`,
		`/* leading */ SELECT 1;`,
		`SELECT /* middle */ 1;`,
		`SELECT 1; /* trailing */`,
		`/* outer /* nested */ still outer */ SELECT 1;`,
		`SELECT '/* not a comment */';`,
		`SELECT "/* not a comment */" FROM t;`,
		`SELECT E'a\'/* not a comment */';`,
		`SELECT 'it''s';`,
		`SELECT "a""b" FROM t;`,
		"DO $$ BEGIN /* inside a body IS a comment */ END $$;",
		"DO $tag$ SELECT '/* still a literal */'; $tag$;",
		`SELECT 'unterminated`,
		`SELECT 1; /* unterminated`,
		`SELECT '¡olé'; /* ünïcode */ SELECT 2;`,
	}

	for _, sql := range inputs {
		if strings.Contains(sql, "--") {
			t.Fatalf("corpus entry has a line comment, which BlockComments does not report: %q", sql)
		}

		t.Run(sql, func(t *testing.T) {
			var want strings.Builder
			prev := 0
			for _, s := range spansOf(sql) {
				want.WriteString(sql[prev:s.Start])
				want.WriteByte(' ')
				prev = s.End
			}
			want.WriteString(sql[prev:])

			if got := c.removeComments(sql); got != want.String() {
				t.Errorf("scanners disagree on what is a comment:\n  input        %q\n"+
					"  checksum     %q\n  preprocessor %q", sql, got, want.String())
			}
		})
	}
}
