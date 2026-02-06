package preprocessor

import (
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
			name:         "pgmi_test with no args",
			input:        "pgmi_test()",
			expectedName: "pgmi_test",
			expectedPat:  "",
		},
		{
			name:         "pgmi_test with NULL",
			input:        "pgmi_test(NULL)",
			expectedName: "pgmi_test",
			expectedPat:  "",
		},
		{
			name:         "pgmi_test with pattern",
			input:        "pgmi_test('./users/**')",
			expectedName: "pgmi_test",
			expectedPat:  "./users/**",
		},
		{
			name:         "pgmi_plan_test with no args",
			input:        "pgmi_plan_test()",
			expectedName: "pgmi_plan_test",
			expectedPat:  "",
		},
		{
			name:         "pgmi_plan_test with pattern",
			input:        "pgmi_plan_test('./api/*')",
			expectedName: "pgmi_plan_test",
			expectedPat:  "./api/*",
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
			name:         "pg_temp.pgmi_test()",
			input:        "pg_temp.pgmi_test()",
			expectedName: "pgmi_test",
			expectedPat:  "",
		},
		{
			name:         "pg_temp.pgmi_test with pattern",
			input:        "pg_temp.pgmi_test('./migrations/**')",
			expectedName: "pgmi_test",
			expectedPat:  "./migrations/**",
		},
		{
			name:         "pg_temp.pgmi_plan_test()",
			input:        "pg_temp.pgmi_plan_test()",
			expectedName: "pgmi_plan_test",
			expectedPat:  "",
		},
		{
			name:         "SELECT pg_temp.pgmi_test()",
			input:        "SELECT pg_temp.pgmi_test();",
			expectedName: "pgmi_test",
			expectedPat:  "",
		},
		{
			name:         "PERFORM pg_temp.pgmi_test()",
			input:        "PERFORM pg_temp.pgmi_test();",
			expectedName: "pgmi_test",
			expectedPat:  "",
		},
		{
			name:         "SELECT pg_temp.pgmi_test with pattern",
			input:        "SELECT pg_temp.pgmi_test('./api/**');",
			expectedName: "pgmi_test",
			expectedPat:  "./api/**",
		},
		{
			name:         "SELECT pg_temp.pgmi_plan_test()",
			input:        "SELECT pg_temp.pgmi_plan_test();",
			expectedName: "pgmi_plan_test",
			expectedPat:  "",
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
			name:  "No whitespace",
			input: "pgmi_test()",
		},
		{
			name:  "Space before paren",
			input: "pgmi_test ()",
		},
		{
			name:  "Space inside parens",
			input: "pgmi_test( )",
		},
		{
			name:  "Spaces around pattern",
			input: "pgmi_test( './pattern/**' )",
		},
		{
			name:  "Tabs and spaces",
			input: "pgmi_test\t(\t'./pattern/**'\t)",
		},
		{
			name:  "Newline in call",
			input: "pgmi_test(\n'./pattern/**'\n)",
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
			name:         "PERFORM statement",
			input:        "PERFORM pgmi_test();",
			expectedName: "pgmi_test",
			expectedLine: 1,
			expectedCol:  9,
		},
		{
			name:         "SELECT statement",
			input:        "SELECT pgmi_test();",
			expectedName: "pgmi_test",
			expectedLine: 1,
			expectedCol:  8,
		},
		{
			name:         "In DO block",
			input:        "DO $$ BEGIN PERFORM pgmi_test(); END $$;",
			expectedName: "pgmi_test",
			expectedLine: 1,
			expectedCol:  21,
		},
		{
			name:         "On second line",
			input:        "-- comment\npgmi_test();",
			expectedName: "pgmi_test",
			expectedLine: 2,
			expectedCol:  1,
		},
		{
			name:         "On third line with offset",
			input:        "line1\nline2\n  pgmi_test();",
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
			input:    "pgmi_test(); pgmi_plan_test();",
			expected: []string{"pgmi_test", "pgmi_plan_test"},
		},
		{
			name:     "Two macros on different lines",
			input:    "pgmi_test();\npgmi_plan_test();",
			expected: []string{"pgmi_test", "pgmi_plan_test"},
		},
		{
			name:     "Same macro twice",
			input:    "pgmi_test('./a/**');\npgmi_test('./b/**');",
			expected: []string{"pgmi_test", "pgmi_test"},
		},
		{
			name:     "Three macros with patterns",
			input:    "pgmi_test();\npgmi_test('./api/**');\npgmi_plan_test('./db/**');",
			expected: []string{"pgmi_test", "pgmi_test", "pgmi_plan_test"},
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

	input := "pgmi_test()"
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

	// Verify the matched text
	matched := input[m.StartPos:m.EndPos]
	if matched != "pgmi_test()" {
		t.Errorf("Matched text = %q, expected %q", matched, "pgmi_test()")
	}
}

func TestMacroDetector_Detect_PositionsWithPattern(t *testing.T) {
	detector := NewMacroDetector()

	input := "SELECT pgmi_test('./users/**');"
	macros := detector.Detect(input)

	if len(macros) != 1 {
		t.Fatalf("Detect() returned %d macros, expected 1", len(macros))
	}

	m := macros[0]
	matched := input[m.StartPos:m.EndPos]
	expected := "pgmi_test('./users/**')"
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
			input: "SELECT pgmi_testing();",
		},
		{
			name:  "Incomplete macro",
			input: "pgmi_test",
		},
		{
			name:  "Missing closing paren",
			input: "pgmi_test(",
		},
		{
			name:  "Wrong prefix",
			input: "my_pgmi_test()",
		},
		// Note: "Comment about macro" case removed because MacroDetector
		// expects comment-stripped input (comments should be removed before detection)
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
			input:       "pgmi_test('./**')",
			expectedPat: "./**",
		},
		{
			name:        "Directory pattern",
			input:       "pgmi_test('./users/__test__/*')",
			expectedPat: "./users/__test__/*",
		},
		{
			name:        "Double star pattern",
			input:       "pgmi_test('./api/**/*.sql')",
			expectedPat: "./api/**/*.sql",
		},
		{
			name:        "Pattern with special chars",
			input:       "pgmi_test('./test-dir/file_name.sql')",
			expectedPat: "./test-dir/file_name.sql",
		},
		{
			name:        "Empty pattern string",
			input:       "pgmi_test('')",
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
SELECT pgmi_test();
COMMIT;`,
			expectedCount: 1,
			expectedNames: []string{"pgmi_test"},
		},
		{
			name: "Advanced template planning mode",
			input: `DO $$
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');
    SELECT pgmi_plan_test('./api/**');
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;`,
			expectedCount: 1,
			expectedNames: []string{"pgmi_plan_test"},
		},
		{
			name: "Mixed direct and pattern with proper SQL syntax",
			input: `SELECT pgmi_test();
SELECT pgmi_test('./users/**');
PERFORM pg_temp.pgmi_test('./api/**');`,
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

	input := "pgmi_test('./a/**'); pgmi_test('./b/**'); pgmi_test('./c/**');"
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

	// Verify positions are ascending
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
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');
    pgmi_test('./users/**');
    pgmi_plan_test('./api/**');
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detector.Detect(input)
	}
}

func BenchmarkMacroDetector_Detect_LargeInput(b *testing.B) {
	detector := NewMacroDetector()

	// Generate large input with several macros
	var input string
	for i := 0; i < 100; i++ {
		input += "SELECT * FROM users WHERE id = 1;\n"
		if i%20 == 0 {
			input += "pgmi_test('./pattern/**');\n"
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detector.Detect(input)
	}
}
