package preprocessor

import (
	"strings"
	"testing"
)

func TestMacroDetector_Detect_BasicCalls(t *testing.T) {
	detector := NewMacroDetector()

	tests := []struct {
		name         string
		input        string
		expectedName string
		expectedPat  string
	}{
		{
			name:         "CALL pgmi_test with no args",
			input:        "CALL pgmi_test()",
			expectedName: "pgmi_test",
			expectedPat:  "",
		},
		{
			name:         "CALL pgmi_test with NULL",
			input:        "CALL pgmi_test(NULL)",
			expectedName: "pgmi_test",
			expectedPat:  "",
		},
		{
			name:         "CALL pgmi_test with pattern",
			input:        "CALL pgmi_test('./users/**')",
			expectedName: "pgmi_test",
			expectedPat:  "./users/**",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			macros := detector.Detect(tt.input)
			if len(macros) != 1 {
				t.Fatalf("Detect() returned %d macros, expected 1", len(macros))
			}
			if macros[0].Name != tt.expectedName {
				t.Errorf("Name = %q, expected %q", macros[0].Name, tt.expectedName)
			}
			if macros[0].Pattern != tt.expectedPat {
				t.Errorf("Pattern = %q, expected %q", macros[0].Pattern, tt.expectedPat)
			}
		})
	}
}

func TestMacroDetector_Detect_SchemaQualified(t *testing.T) {
	detector := NewMacroDetector()

	tests := []struct {
		name         string
		input        string
		expectedName string
		expectedPat  string
	}{
		{
			name:         "CALL pg_temp.pgmi_test()",
			input:        "CALL pg_temp.pgmi_test()",
			expectedName: "pgmi_test",
			expectedPat:  "",
		},
		{
			name:         "CALL pg_temp.pgmi_test with pattern",
			input:        "CALL pg_temp.pgmi_test('./migrations/**')",
			expectedName: "pgmi_test",
			expectedPat:  "./migrations/**",
		},
		{
			name:         "CALL pg_temp.pgmi_test with semicolon",
			input:        "CALL pg_temp.pgmi_test();",
			expectedName: "pgmi_test",
			expectedPat:  "",
		},
		{
			name:         "CALL pg_temp.pgmi_test with pattern and semicolon",
			input:        "CALL pg_temp.pgmi_test('./api/**');",
			expectedName: "pgmi_test",
			expectedPat:  "./api/**",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			macros := detector.Detect(tt.input)
			if len(macros) != 1 {
				t.Fatalf("Detect() returned %d macros, expected 1", len(macros))
			}
			if macros[0].Name != tt.expectedName {
				t.Errorf("Name = %q, expected %q", macros[0].Name, tt.expectedName)
			}
			if macros[0].Pattern != tt.expectedPat {
				t.Errorf("Pattern = %q, expected %q", macros[0].Pattern, tt.expectedPat)
			}
		})
	}
}

func TestMacroDetector_Detect_WhitespaceVariations(t *testing.T) {
	detector := NewMacroDetector()

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "Minimal whitespace",
			input: "CALL pgmi_test()",
		},
		{
			name:  "Space after CALL",
			input: "CALL  pgmi_test()",
		},
		{
			name:  "Space before paren",
			input: "CALL pgmi_test ()",
		},
		{
			name:  "Space inside parens",
			input: "CALL pgmi_test( )",
		},
		{
			name:  "Spaces around pattern",
			input: "CALL pgmi_test( './pattern/**' )",
		},
		{
			name:  "Tabs and spaces",
			input: "CALL\tpgmi_test\t(\t'./pattern/**'\t)",
		},
		{
			name:  "Newline after CALL",
			input: "CALL\npgmi_test('./pattern/**')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			macros := detector.Detect(tt.input)
			if len(macros) != 1 {
				t.Fatalf("Detect() returned %d macros, expected 1 for input %q", len(macros), tt.input)
			}
			if macros[0].Name != "pgmi_test" {
				t.Errorf("Name = %q, expected %q", macros[0].Name, "pgmi_test")
			}
		})
	}
}

func TestMacroDetector_Detect_InContext(t *testing.T) {
	detector := NewMacroDetector()

	tests := []struct {
		name         string
		input        string
		expectedName string
		expectedLine int
		expectedCol  int
	}{
		{
			name:         "CALL statement",
			input:        "CALL pgmi_test();",
			expectedName: "pgmi_test",
			expectedLine: 1,
			expectedCol:  1,
		},
		{
			name:         "In DO block",
			input:        "DO $$ BEGIN CALL pgmi_test(); END $$;",
			expectedName: "pgmi_test",
			expectedLine: 1,
			expectedCol:  13, // Position of 'C' in CALL
		},
		{
			name:         "On second line",
			input:        "-- comment\nCALL pgmi_test();",
			expectedName: "pgmi_test",
			expectedLine: 2,
			expectedCol:  1,
		},
		{
			name:         "On third line with offset",
			input:        "line1\nline2\n  CALL pgmi_test();",
			expectedName: "pgmi_test",
			expectedLine: 3,
			expectedCol:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			macros := detector.Detect(tt.input)
			if len(macros) != 1 {
				t.Fatalf("Detect() returned %d macros, expected 1", len(macros))
			}
			if macros[0].Name != tt.expectedName {
				t.Errorf("Name = %q, expected %q", macros[0].Name, tt.expectedName)
			}
			if macros[0].Line != tt.expectedLine {
				t.Errorf("Line = %d, expected %d", macros[0].Line, tt.expectedLine)
			}
			if macros[0].Column != tt.expectedCol {
				t.Errorf("Column = %d, expected %d", macros[0].Column, tt.expectedCol)
			}
		})
	}
}

func TestMacroDetector_Detect_MultipleMacros(t *testing.T) {
	detector := NewMacroDetector()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "Two macros on same line",
			input:    "CALL pgmi_test(); CALL pgmi_test('./users/**');",
			expected: []string{"pgmi_test", "pgmi_test"},
		},
		{
			name:     "Two macros on different lines",
			input:    "CALL pgmi_test();\nCALL pgmi_test('./api/**');",
			expected: []string{"pgmi_test", "pgmi_test"},
		},
		{
			name:     "Same macro twice",
			input:    "CALL pgmi_test('./a/**');\nCALL pgmi_test('./b/**');",
			expected: []string{"pgmi_test", "pgmi_test"},
		},
		{
			name:     "Three macros with patterns",
			input:    "CALL pgmi_test();\nCALL pgmi_test('./api/**');\nCALL pgmi_test('./db/**');",
			expected: []string{"pgmi_test", "pgmi_test", "pgmi_test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			macros := detector.Detect(tt.input)
			if len(macros) != len(tt.expected) {
				t.Fatalf("Detect() returned %d macros, expected %d", len(macros), len(tt.expected))
			}
			for i, name := range tt.expected {
				if macros[i].Name != name {
					t.Errorf("macros[%d].Name = %q, expected %q", i, macros[i].Name, name)
				}
			}
		})
	}
}

func TestMacroDetector_Detect_Positions(t *testing.T) {
	detector := NewMacroDetector()

	input := "CALL pgmi_test()"
	macros := detector.Detect(input)

	if len(macros) != 1 {
		t.Fatalf("Detect() returned %d macros, expected 1", len(macros))
	}

	m := macros[0]
	if m.StartPos != 0 {
		t.Errorf("StartPos = %d, expected 0", m.StartPos)
	}
	if m.EndPos != len(input) {
		t.Errorf("EndPos = %d, expected %d", m.EndPos, len(input))
	}

	matched := input[m.StartPos:m.EndPos]
	if matched != "CALL pgmi_test()" {
		t.Errorf("Matched text = %q, expected %q", matched, "CALL pgmi_test()")
	}
}

func TestMacroDetector_Detect_PositionsWithPattern(t *testing.T) {
	detector := NewMacroDetector()

	input := "CALL pgmi_test('./users/**');"
	macros := detector.Detect(input)

	if len(macros) != 1 {
		t.Fatalf("Detect() returned %d macros, expected 1", len(macros))
	}

	m := macros[0]
	matched := input[m.StartPos:m.EndPos]
	expected := "CALL pgmi_test('./users/**');"
	if matched != expected {
		t.Errorf("Matched text = %q, expected %q", matched, expected)
	}
}

func TestMacroDetector_Detect_NoMacros(t *testing.T) {
	detector := NewMacroDetector()

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "Empty string",
			input: "",
		},
		{
			name:  "Regular SQL",
			input: "SELECT * FROM users;",
		},
		{
			name:  "Similar but not macro",
			input: "CALL pgmi_testing();",
		},
		{
			name:  "Incomplete macro",
			input: "CALL pgmi_test",
		},
		{
			name:  "Missing closing paren",
			input: "CALL pgmi_test(",
		},
		{
			name:  "Wrong prefix (SELECT not allowed)",
			input: "SELECT pgmi_test()",
		},
		{
			name:  "Wrong prefix (PERFORM not allowed)",
			input: "PERFORM pgmi_test()",
		},
		{
			name:  "Bare macro without CALL",
			input: "pgmi_test()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			macros := detector.Detect(tt.input)
			if len(macros) != 0 {
				t.Errorf("Detect() returned %d macros, expected 0 for input %q", len(macros), tt.input)
			}
		})
	}
}

func TestMacroDetector_Detect_PatternVariations(t *testing.T) {
	detector := NewMacroDetector()

	tests := []struct {
		name        string
		input       string
		expectedPat string
	}{
		{
			name:        "Simple glob",
			input:       "CALL pgmi_test('./**')",
			expectedPat: "./**",
		},
		{
			name:        "Directory pattern",
			input:       "CALL pgmi_test('./users/__test__/*')",
			expectedPat: "./users/__test__/*",
		},
		{
			name:        "Double star pattern",
			input:       "CALL pgmi_test('./api/**/*.sql')",
			expectedPat: "./api/**/*.sql",
		},
		{
			name:        "Pattern with special chars",
			input:       "CALL pgmi_test('./test-dir/file_name.sql')",
			expectedPat: "./test-dir/file_name.sql",
		},
		{
			name:        "Empty pattern string",
			input:       "CALL pgmi_test('')",
			expectedPat: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			macros := detector.Detect(tt.input)
			if len(macros) != 1 {
				t.Fatalf("Detect() returned %d macros, expected 1", len(macros))
			}
			if macros[0].Pattern != tt.expectedPat {
				t.Errorf("Pattern = %q, expected %q", macros[0].Pattern, tt.expectedPat)
			}
		})
	}
}

func TestMacroDetector_Detect_RealWorldExamples(t *testing.T) {
	detector := NewMacroDetector()

	tests := []struct {
		name          string
		input         string
		expectedCount int
		expectedNames []string
	}{
		{
			name: "Basic template direct mode",
			input: `BEGIN;
DO $$ DECLARE v_file RECORD; BEGIN
    FOR v_file IN SELECT path FROM pgmi_source LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
CALL pgmi_test();
COMMIT;`,
			expectedCount: 1,
			expectedNames: []string{"pgmi_test"},
		},
		{
			name: "Advanced template direct mode",
			input: `DO $$
BEGIN
    CALL pgmi_test('./api/**');
END $$;`,
			expectedCount: 1,
			expectedNames: []string{"pgmi_test"},
		},
		{
			name: "Multiple CALL statements",
			input: `CALL pgmi_test();
CALL pgmi_test('./users/**');
CALL pg_temp.pgmi_test('./api/**');`,
			expectedCount: 3,
			expectedNames: []string{"pgmi_test", "pgmi_test", "pgmi_test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			macros := detector.Detect(tt.input)
			if len(macros) != tt.expectedCount {
				t.Fatalf("Detect() returned %d macros, expected %d", len(macros), tt.expectedCount)
			}
			for i, name := range tt.expectedNames {
				if macros[i].Name != name {
					t.Errorf("macros[%d].Name = %q, expected %q", i, macros[i].Name, name)
				}
			}
		})
	}
}

func TestMacroDetector_Detect_OrderPreserved(t *testing.T) {
	detector := NewMacroDetector()

	input := "CALL pgmi_test('./a/**'); CALL pgmi_test('./b/**'); CALL pgmi_test('./c/**');"
	macros := detector.Detect(input)

	if len(macros) != 3 {
		t.Fatalf("Detect() returned %d macros, expected 3", len(macros))
	}

	expectedPatterns := []string{"./a/**", "./b/**", "./c/**"}
	for i, pat := range expectedPatterns {
		if macros[i].Pattern != pat {
			t.Errorf("macros[%d].Pattern = %q, expected %q", i, macros[i].Pattern, pat)
		}
	}

	for i := 1; i < len(macros); i++ {
		if macros[i].StartPos <= macros[i-1].StartPos {
			t.Errorf("Macros not in ascending order: macros[%d].StartPos=%d <= macros[%d].StartPos=%d",
				i, macros[i].StartPos, i-1, macros[i-1].StartPos)
		}
	}
}

func BenchmarkMacroDetector_Detect(b *testing.B) {
	detector := NewMacroDetector()
	input := `DO $$
BEGIN
    CALL pgmi_test('./users/**');
    CALL pgmi_test('./api/**');
END $$;`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detector.Detect(input)
	}
}

func BenchmarkMacroDetector_Detect_LargeInput(b *testing.B) {
	detector := NewMacroDetector()

	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("SELECT * FROM users WHERE id = 1;\n")
		if i%20 == 0 {
			sb.WriteString("CALL pgmi_test('./pattern/**');\n")
		}
	}
	input := sb.String()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detector.Detect(input)
	}
}

func TestMacroDetector_Detect_WithCallback(t *testing.T) {
	detector := NewMacroDetector()

	tests := []struct {
		name     string
		input    string
		wantName string
		wantPat  string
		wantCb   string
	}{
		{
			name:     "pattern and callback",
			input:    "CALL pgmi_test('./u/**', 'pg_temp.cb')",
			wantName: "pgmi_test",
			wantPat:  "./u/**",
			wantCb:   "pg_temp.cb",
		},
		{
			name:     "NULL pattern with callback",
			input:    "CALL pgmi_test(NULL, 'pg_temp.x')",
			wantName: "pgmi_test",
			wantPat:  "",
			wantCb:   "pg_temp.x",
		},
		{
			name:     "pattern only - no callback",
			input:    "CALL pgmi_test('./a/**')",
			wantName: "pgmi_test",
			wantPat:  "./a/**",
			wantCb:   "",
		},
		{
			name:     "no args - no callback",
			input:    "CALL pgmi_test()",
			wantName: "pgmi_test",
			wantPat:  "",
			wantCb:   "",
		},
		{
			name:     "spaces around args",
			input:    "CALL pgmi_test( './a/**' , 'pg_temp.cb' )",
			wantName: "pgmi_test",
			wantPat:  "./a/**",
			wantCb:   "pg_temp.cb",
		},
		{
			name:     "schema-qualified callback",
			input:    "CALL pgmi_test('./x/**', 'myschema.reporter')",
			wantName: "pgmi_test",
			wantPat:  "./x/**",
			wantCb:   "myschema.reporter",
		},
		{
			name:     "callback with NULL pattern and semicolon",
			input:    "CALL pgmi_test(NULL, 'pg_temp.my_callback');",
			wantName: "pgmi_test",
			wantPat:  "",
			wantCb:   "pg_temp.my_callback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			macros := detector.Detect(tt.input)
			if len(macros) != 1 {
				t.Fatalf("Detect() returned %d macros, expected 1", len(macros))
			}
			if macros[0].Name != tt.wantName {
				t.Errorf("Name = %q, expected %q", macros[0].Name, tt.wantName)
			}
			if macros[0].Pattern != tt.wantPat {
				t.Errorf("Pattern = %q, expected %q", macros[0].Pattern, tt.wantPat)
			}
			if macros[0].Callback != tt.wantCb {
				t.Errorf("Callback = %q, expected %q", macros[0].Callback, tt.wantCb)
			}
		})
	}
}
