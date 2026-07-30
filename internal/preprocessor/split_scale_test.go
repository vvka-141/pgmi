package preprocessor

import (
	"strings"
	"testing"
)

func buildTailHeavyScript(statements int) string {
	var b strings.Builder
	b.WriteString("BEGIN;\nSELECT 1;\nCOMMIT;\n")
	for range statements {
		b.WriteString("UPDATE t SET c = c + 1 WHERE id = ")
		b.WriteString(strings.Repeat("0", 8))
		b.WriteString(";\n")
	}
	return b.String()
}

// Masking every preceding byte made the split quadratic in script size: this
// shape produced 352 MB of units for a 176 KB script, all of it also sent to
// the server. The prefix only has to reproduce line and column.
func TestSplitExecutionUnitsTailHeavyStaysLinear(t *testing.T) {
	const statements = 4000
	sql := buildTailHeavyScript(statements)

	units := SplitExecutionUnits(sql)
	if len(units) != statements+1 {
		t.Fatalf("got %d units, want %d", len(units), statements+1)
	}

	var total int
	for _, u := range units {
		total += len(u)
	}

	// Newline-only padding is still one line per preceding statement, but the
	// constant is small enough that a realistic script stays in kilobytes.
	// Full-byte masking scored 2000x on this input.
	const maxRatio = 60
	if ratio := float64(total) / float64(len(sql)); ratio > maxRatio {
		t.Errorf("units total %d bytes for a %d byte script (%.1fx), want under %dx",
			total, len(sql), ratio, maxRatio)
	}
}

// The cheap prefix must be indistinguishable from full-byte masking to
// everything that reads it: PostgreSQL reports a position into the unit, and
// LocateError turns that into a line and column.
func TestPadPrefixResolvesLikeFullMask(t *testing.T) {
	fullMask := func(sql string, end int) string {
		pad := make([]byte, end)
		for i := range end {
			if sql[i] == '\n' {
				pad[i] = '\n'
			} else {
				pad[i] = ' '
			}
		}
		return string(pad)
	}

	// line/column exactly as pkg/pgmi resolvePosition computes them.
	locate := func(s string, pos int) (line, col int) {
		line, col = 1, 1
		for _, r := range []rune(s)[:pos-1] {
			if r == '\n' {
				line++
				col = 1
				continue
			}
			col++
		}
		return line, col
	}

	scripts := []string{
		"BEGIN;\nSELECT 1;\nCOMMIT;\nVACUUM;\n",
		"BEGIN;\r\nSELECT 1;\r\nCOMMIT;\r\nVACUUM;\r\n",
		"COMMIT; SELECT 1; SELECT 2;",
		"BEGIN;\nSELECT 'ünïcodé';\nCOMMIT;\nSELECT 3;\n",
		"\n\n\nCOMMIT;\n\n\nSELECT 4;\n",
	}

	for _, sql := range scripts {
		for end := range len(sql) + 1 {
			cheap := padPrefix(sql, end)
			full := fullMask(sql, end)

			// A position pointing at the first character after the prefix.
			cheapLine, cheapCol := locate(cheap+"X", len([]rune(cheap))+1)
			fullLine, fullCol := locate(full+"X", len([]rune(full))+1)

			if cheapLine != fullLine || cheapCol != fullCol {
				t.Fatalf("end=%d in %q: cheap prefix resolves to %d:%d, full mask to %d:%d",
					end, sql, cheapLine, cheapCol, fullLine, fullCol)
			}
		}
	}
}
