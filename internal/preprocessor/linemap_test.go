package preprocessor

import (
	"strings"
	"testing"
)

func TestLineMap_MapsExpandedLinesBackToTheFile(t *testing.T) {
	call := "CALL pgmi_test();"
	sql := "BEGIN;\nSAVEPOINT s;\n" + call + "\nROLLBACK TO SAVEPOINT s;\nCOMMIT;\nSELECT 1/0;\n"
	start := strings.Index(sql, call)
	macros := []MacroCall{{StartPos: start, EndPos: start + len(call)}}
	gen := "A;\nB;\nC;\nD;"
	m := buildLineMap(sql, macros, map[int]string{start: gen})
	expanded := sql[:start] + gen + sql[start+len(call):]

	for _, tc := range []struct {
		expandedLine, want int
		inMacro            bool
	}{
		{1, 1, false},
		{2, 2, false},
		{3, 3, true},
		{6, 3, true},
		{7, 4, false},
		{9, 6, false},
	} {
		got, inMacro := m.OriginalLine(tc.expandedLine)
		if got != tc.want || inMacro != tc.inMacro {
			t.Errorf("expanded line %d (%q): got %d inMacro=%v, want %d inMacro=%v",
				tc.expandedLine, strings.Split(expanded, "\n")[tc.expandedLine-1], got, inMacro, tc.want, tc.inMacro)
		}
	}
}

func TestLineMap_TwoMacrosAccumulateTheirShift(t *testing.T) {
	call := "CALL pgmi_test();"
	sql := call + "\nX;\n" + call + "\nY;\n"
	first, second := 0, strings.LastIndex(sql, call)
	m := buildLineMap(sql, []MacroCall{
		{StartPos: second, EndPos: second + len(call)},
		{StartPos: first, EndPos: first + len(call)},
	}, map[int]string{first: "a;\nb;", second: "c;\nd;\ne;"})

	for expanded, want := range map[int]int{3: 2, 4: 3, 6: 3, 7: 4} {
		if got, _ := m.OriginalLine(expanded); got != want {
			t.Errorf("expanded line %d: got %d, want %d", expanded, got, want)
		}
	}
}
