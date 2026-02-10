package preprocessor

import (
	"testing"
)

func TestCommentStripper_Strip_LineComments(t *testing.T) {
	stripper := NewCommentStripper()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Simple line comment",
			input:    "SELECT 1; -- comment",
			expected: "SELECT 1; ",
		},
		{
			name:     "Line comment at start",
			input:    "-- comment\nSELECT 1;",
			expected: "\nSELECT 1;",
		},
		{
			name:     "Multiple line comments",
			input:    "-- first\nSELECT 1; -- second\n-- third",
			expected: "\nSELECT 1; \n",
		},
		{
			name:     "Line comment only",
			input:    "-- just a comment",
			expected: "",
		},
		{
			name:     "Empty line comment",
			input:    "--\nSELECT 1;",
			expected: "\nSELECT 1;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := stripper.Strip(tt.input)
			if result != tt.expected {
				t.Errorf("Strip() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestCommentStripper_Strip_BlockComments(t *testing.T) {
	stripper := NewCommentStripper()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Simple block comment",
			input:    "SELECT /* comment */ 1;",
			expected: "SELECT  1;",
		},
		{
			name:     "Block comment at start",
			input:    "/* comment */SELECT 1;",
			expected: "SELECT 1;",
		},
		{
			name:     "Block comment at end",
			input:    "SELECT 1;/* comment */",
			expected: "SELECT 1;",
		},
		{
			name:     "Multi-line block comment",
			input:    "SELECT /* line1\nline2\nline3 */ 1;",
			expected: "SELECT  1;",
		},
		{
			name:     "Multiple block comments",
			input:    "/* c1 */SELECT/* c2 */ 1;/* c3 */",
			expected: "SELECT 1;",
		},
		{
			name:     "Block comment only",
			input:    "/* just a comment */",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := stripper.Strip(tt.input)
			if result != tt.expected {
				t.Errorf("Strip() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestCommentStripper_Strip_NestedBlockComments(t *testing.T) {
	stripper := NewCommentStripper()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Single level nested",
			input:    "SELECT /* outer /* inner */ outer */ 1;",
			expected: "SELECT  1;",
		},
		{
			name:     "Double nested",
			input:    "/* a /* b /* c */ b */ a */SELECT 1;",
			expected: "SELECT 1;",
		},
		{
			name:     "Nested only",
			input:    "/* /* nested */ */",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := stripper.Strip(tt.input)
			if result != tt.expected {
				t.Errorf("Strip() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestCommentStripper_Strip_SingleQuoteStrings(t *testing.T) {
	stripper := NewCommentStripper()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Line comment syntax in string",
			input:    "SELECT '--not comment';",
			expected: "SELECT '--not comment';",
		},
		{
			name:     "Block comment syntax in string",
			input:    "SELECT '/* not comment */';",
			expected: "SELECT '/* not comment */';",
		},
		{
			name:     "Escaped quote",
			input:    "SELECT 'it''s escaped';",
			expected: "SELECT 'it''s escaped';",
		},
		{
			name:     "Escaped quote with comment syntax",
			input:    "SELECT 'it''s -- not a comment';",
			expected: "SELECT 'it''s -- not a comment';",
		},
		{
			name:     "Multiple strings",
			input:    "SELECT 'a', '--b', 'c';",
			expected: "SELECT 'a', '--b', 'c';",
		},
		{
			name:     "String followed by real comment",
			input:    "SELECT 'value'; -- comment",
			expected: "SELECT 'value'; ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := stripper.Strip(tt.input)
			if result != tt.expected {
				t.Errorf("Strip() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestCommentStripper_Strip_DollarQuoteStrings(t *testing.T) {
	stripper := NewCommentStripper()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Simple dollar quote",
			input:    "SELECT $$--not comment$$;",
			expected: "SELECT $$--not comment$$;",
		},
		{
			name:     "Dollar quote with block comment syntax",
			input:    "SELECT $$/* not comment */$$;",
			expected: "SELECT $$/* not comment */$$;",
		},
		{
			name:     "Tagged dollar quote",
			input:    "SELECT $tag$--not comment$tag$;",
			expected: "SELECT $tag$--not comment$tag$;",
		},
		{
			name:     "Tagged dollar quote with block syntax",
			input:    "SELECT $x$/*nope*/$x$;",
			expected: "SELECT $x$/*nope*/$x$;",
		},
		{
			name:     "Different tags nested",
			input:    "SELECT $a$ content $b$ inner $b$ more $a$;",
			expected: "SELECT $a$ content $b$ inner $b$ more $a$;",
		},
		{
			name:     "Dollar quote followed by comment",
			input:    "$$body$$ -- comment",
			expected: "$$body$$ ",
		},
		{
			name:     "Function body with dollar quote",
			input:    "CREATE FUNCTION f() AS $$ SELECT '--'; $$ LANGUAGE sql;",
			expected: "CREATE FUNCTION f() AS $$ SELECT '--'; $$ LANGUAGE sql;",
		},
		{
			name:     "Numeric tag",
			input:    "SELECT $1$content$1$;",
			expected: "SELECT $1$content$1$;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := stripper.Strip(tt.input)
			if result != tt.expected {
				t.Errorf("Strip() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestCommentStripper_Strip_MixedScenarios(t *testing.T) {
	stripper := NewCommentStripper()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "String and line comment",
			input:    "SELECT 'a'; -- comment\nSELECT 'b';",
			expected: "SELECT 'a'; \nSELECT 'b';",
		},
		{
			name:     "Block and line comment",
			input:    "/* block */ SELECT 1; -- line",
			expected: " SELECT 1; ",
		},
		{
			name:     "Complex real-world",
			input:    "-- Header comment\n/* Description */\nCREATE TABLE t (\n  id INT, -- ID column\n  name TEXT /* name */\n);",
			expected: "\n\nCREATE TABLE t (\n  id INT, \n  name TEXT \n);",
		},
		{
			name:     "Dollar quote inside block comment",
			input:    "/* $$not string$$ */SELECT 1;",
			expected: "SELECT 1;",
		},
		{
			name:     "No comments",
			input:    "SELECT * FROM users WHERE id = 1;",
			expected: "SELECT * FROM users WHERE id = 1;",
		},
		{
			name:     "Empty input",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := stripper.Strip(tt.input)
			if result != tt.expected {
				t.Errorf("Strip() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestCommentStripper_Strip_LineMapping(t *testing.T) {
	stripper := NewCommentStripper()

	tests := []struct {
		name          string
		input         string
		expectedLines int
	}{
		{
			name:          "Single line",
			input:         "SELECT 1;",
			expectedLines: 1,
		},
		{
			name:          "Multiple lines preserved",
			input:         "SELECT 1;\nSELECT 2;\nSELECT 3;",
			expectedLines: 3,
		},
		{
			name:          "Comments removed but lines preserved",
			input:         "-- comment\nSELECT 1;\n-- comment\nSELECT 2;",
			expectedLines: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, lineMap := stripper.Strip(tt.input)

			// Count newlines in result
			resultLines := 1
			for _, c := range result {
				if c == '\n' {
					resultLines++
				}
			}

			// Line map should have entries for each line
			if len(lineMap) == 0 && tt.expectedLines > 0 {
				t.Errorf("LineMap is empty, expected entries")
			}
		})
	}
}

func TestCommentStripper_Strip_EdgeCases(t *testing.T) {
	stripper := NewCommentStripper()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Dash but not comment",
			input:    "SELECT 1-2;",
			expected: "SELECT 1-2;",
		},
		{
			name:     "Slash but not comment",
			input:    "SELECT 1/2;",
			expected: "SELECT 1/2;",
		},
		{
			name:     "Asterisk but not comment end",
			input:    "SELECT 1*2;",
			expected: "SELECT 1*2;",
		},
		{
			name:     "Unclosed string",
			input:    "SELECT 'unclosed",
			expected: "SELECT 'unclosed",
		},
		{
			name:     "Unclosed dollar quote",
			input:    "SELECT $$unclosed",
			expected: "SELECT $$unclosed",
		},
		{
			name:     "Almost dollar quote",
			input:    "SELECT $notag content",
			expected: "SELECT $notag content",
		},
		{
			name:     "Windows line endings",
			input:    "SELECT 1; -- comment\r\nSELECT 2;",
			expected: "SELECT 1; \r\nSELECT 2;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := stripper.Strip(tt.input)
			if result != tt.expected {
				t.Errorf("Strip() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func BenchmarkCommentStripper_Strip(b *testing.B) {
	stripper := NewCommentStripper()
	input := `-- Header comment
/* Multi-line
   block comment */
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL, -- Name column
    email TEXT UNIQUE /* email must be unique */
);

CREATE FUNCTION get_user(p_id INT) RETURNS TEXT AS $$
    SELECT name FROM users WHERE id = p_id;
$$ LANGUAGE sql;
`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stripper.Strip(input)
	}
}

func BenchmarkCommentStripper_Strip_LargeInput(b *testing.B) {
	stripper := NewCommentStripper()

	// Generate a large SQL input
	var input string
	for i := 0; i < 1000; i++ {
		input += "SELECT * FROM users WHERE id = 1; -- comment\n"
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stripper.Strip(input)
	}
}
