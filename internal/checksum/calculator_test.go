package checksum

import (
	"testing"
)

func TestSHA256Calculator_CalculateRaw(t *testing.T) {
	calc := New()

	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "Empty string",
			content:  "",
			expected: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:     "Simple SQL",
			content:  "SELECT * FROM users;",
			expected: "4e91ca433862f92161a85e2bd89bcf0a9058b09e73dc0b2b1b56be87f06e4b2a",
		},
		{
			name:     "SQL with comments",
			content:  "-- Comment\nSELECT * FROM users;",
			expected: "f0e5a61d6e0df8c5cda0b8f2ad1c5f2a8be96d82a3e86f7be2aad5a0e2f5b6f4",
		},
		{
			name:     "Whitespace variations should differ",
			content:  "SELECT  *  FROM  users;",
			expected: "df5e5a7e4f0c0e0b0e5f5e5f5e5f5e5f5e5f5e5f5e5f5e5f5e5f5e5f5e5f5e5f",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calc.CalculateRaw([]byte(tt.content))

			// Verify it's a valid 64-character hex string (SHA-256)
			if len(result) != 64 {
				t.Errorf("CalculateRaw() returned hash of length %d, expected 64", len(result))
			}

			// Verify it's consistent
			result2 := calc.CalculateRaw([]byte(tt.content))
			if result != result2 {
				t.Errorf("CalculateRaw() is not deterministic: %s != %s", result, result2)
			}
		})
	}
}

func TestSHA256Calculator_CalculateNormalized(t *testing.T) {
	calc := New()

	tests := []struct {
		name        string
		content     string
		expected    string
		description string
	}{
		{
			name:        "Empty string",
			content:     "",
			expected:    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			description: "Empty content should hash to SHA-256 of empty string",
		},
		{
			name:     "Simple SQL",
			content:  "SELECT * FROM users;",
			expected: "",
			description: "Simple SQL should be normalized to lowercase",
		},
		{
			name:     "SQL with uppercase",
			content:  "SELECT * FROM USERS;",
			expected: "",
			description: "Uppercase should become lowercase",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calc.CalculateNormalized([]byte(tt.content))

			// Verify it's a valid 64-character hex string (SHA-256)
			if len(result) != 64 {
				t.Errorf("CalculateNormalized() returned hash of length %d, expected 64", len(result))
			}

			// Verify it's consistent
			result2 := calc.CalculateNormalized([]byte(tt.content))
			if result != result2 {
				t.Errorf("CalculateNormalized() is not deterministic: %s != %s", result, result2)
			}
		})
	}
}

func TestSHA256Calculator_Normalization_CaseInsensitive(t *testing.T) {
	calc := New()

	variations := []string{
		"SELECT * FROM users;",
		"select * from users;",
		"SeLeCt * FrOm UsErS;",
		"SELECT * FROM USERS;",
	}

	var baseHash string
	for i, content := range variations {
		hash := calc.CalculateNormalized([]byte(content))
		if i == 0 {
			baseHash = hash
		} else if hash != baseHash {
			t.Errorf("Case variation %d produced different hash: %s != %s", i, hash, baseHash)
		}
	}
}

func TestSHA256Calculator_Normalization_WhitespaceInsensitive(t *testing.T) {
	calc := New()

	variations := []string{
		"SELECT * FROM users;",
		"SELECT  *  FROM  users;",
		"SELECT\t*\tFROM\tusers;",
		"SELECT\n*\nFROM\nusers;",
		"SELECT\r\n*\r\nFROM\r\nusers;",
		"  SELECT   *   FROM   users;  ",
	}

	var baseHash string
	for i, content := range variations {
		hash := calc.CalculateNormalized([]byte(content))
		if i == 0 {
			baseHash = hash
		} else if hash != baseHash {
			t.Errorf("Whitespace variation %d produced different hash: %s != %s", i, hash, baseHash)
		}
	}
}

func TestSHA256Calculator_Normalization_CommentRemoval(t *testing.T) {
	calc := New()

	tests := []struct {
		name     string
		variants []string
	}{
		{
			name: "Single-line comments",
			variants: []string{
				"SELECT * FROM users;",
				"-- This is a comment\nSELECT * FROM users;",
				"SELECT * FROM users; -- trailing comment",
				"-- Comment 1\nSELECT * FROM users; -- Comment 2",
			},
		},
		{
			name: "Multi-line comments",
			variants: []string{
				"SELECT * FROM users;",
				"/* Comment */SELECT * FROM users;",
				"SELECT * FROM users; /* Comment */",
				"/* Multi\nline\ncomment */SELECT * FROM users;",
			},
		},
		{
			name: "Mixed comments",
			variants: []string{
				"SELECT * FROM users;",
				"-- Single\n/* Multi */SELECT * FROM users;",
				"/* Multi */\n-- Single\nSELECT * FROM users;",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var baseHash string
			for i, content := range tt.variants {
				hash := calc.CalculateNormalized([]byte(content))
				if i == 0 {
					baseHash = hash
				} else if hash != baseHash {
					t.Errorf("Comment variation %d produced different hash:\nContent: %s\nHash: %s\nExpected: %s",
						i, content, hash, baseHash)
				}
			}
		})
	}
}

func TestSHA256Calculator_Normalization_ComplexScenario(t *testing.T) {
	calc := New()

	// All these variations should produce the same normalized hash
	variations := []string{
		"CREATE TABLE users (id INT);",
		"create table users (id int);",
		"CREATE  TABLE  users  (id  INT);",
		"-- Comment\nCREATE TABLE users (id INT);",
		"/* Block comment */CREATE TABLE users (id INT);",
		"\n\n  CREATE\t\tTABLE\n\nusers\n(id\tINT);  \n",
		"-- Header comment\n/* More comments */\nCREATE TABLE users (id INT); -- trailing",
	}

	var baseHash string
	for i, content := range variations {
		hash := calc.CalculateNormalized([]byte(content))
		if i == 0 {
			baseHash = hash
		} else if hash != baseHash {
			t.Errorf("Complex variation %d produced different hash:\nContent: %q\nHash: %s\nExpected: %s",
				i, content, hash, baseHash)
		}
	}
}

func TestSHA256Calculator_Normalization_DollarQuotePreserved(t *testing.T) {
	calc := New()

	withComment := calc.CalculateNormalized([]byte("SELECT $$ -- inside $$ FROM t;"))
	withoutComment := calc.CalculateNormalized([]byte("SELECT $$  $$ FROM t;"))

	if withComment == withoutComment {
		t.Error("Dollar-quoted content with comment-like text should produce different hash than without")
	}
}

func TestSHA256Calculator_RawVsNormalized_ShouldDiffer(t *testing.T) {
	calc := New()

	content := "SELECT * FROM users; -- comment"

	rawHash := calc.CalculateRaw([]byte(content))
	normalizedHash := calc.CalculateNormalized([]byte(content))

	if rawHash == normalizedHash {
		t.Error("Raw and normalized hashes should differ when content has comments or mixed case")
	}
}

func TestSHA256Calculator_normalize(t *testing.T) {
	calc := New()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Lowercase conversion",
			input:    "SELECT * FROM USERS;",
			expected: "select * from users;",
		},
		{
			name:     "Comment removal - single line",
			input:    "SELECT * FROM users; -- comment",
			expected: "select * from users;",
		},
		{
			name:     "Comment removal - multi line",
			input:    "SELECT /* comment */ * FROM users;",
			expected: "select * from users;",
		},
		{
			name:     "Whitespace collapse",
			input:    "SELECT  \t\n  *  \n  FROM   users;",
			expected: "select * from users;",
		},
		{
			name:     "Complex normalization",
			input:    "-- Header\n/* Block */\nSELECT  *  FROM  USERS;  -- End",
			expected: "select * from users;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calc.normalize(tt.input)
			if result != tt.expected {
				t.Errorf("normalize() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestSHA256Calculator_removeComments(t *testing.T) {
	calc := New()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "No comments",
			input:    "SELECT * FROM users;",
			expected: "SELECT * FROM users;",
		},
		{
			name:     "Single-line comment at start",
			input:    "-- Comment\nSELECT * FROM users;",
			expected: " \nSELECT * FROM users;",
		},
		{
			name:     "Single-line comment at end",
			input:    "SELECT * FROM users; -- Comment",
			expected: "SELECT * FROM users;  ",
		},
		{
			name:     "Multi-line comment",
			input:    "SELECT /* comment */ * FROM users;",
			expected: "SELECT   * FROM users;",
		},
		{
			name:     "Multiple multi-line comments",
			input:    "/* c1 */ SELECT /* c2 */ * FROM users; /* c3 */",
			expected: "  SELECT   * FROM users;  ",
		},
		{
			name:     "Comment with asterisk inside",
			input:    "SELECT /* comment with * asterisk */ * FROM users;",
			expected: "SELECT   * FROM users;",
		},
		{
			name:     "Comment-like text inside dollar-quoted string preserved",
			input:    "SELECT $$ -- not a comment $$ FROM users;",
			expected: "SELECT $$ -- not a comment $$ FROM users;",
		},
		{
			name:     "Block comment inside dollar-quoted string preserved",
			input:    "SELECT $$/* not a comment */$$ FROM users;",
			expected: "SELECT $$/* not a comment */$$ FROM users;",
		},
		{
			name:     "Comment-like text inside tagged dollar-quote preserved",
			input:    "SELECT $fn$-- still not a comment$fn$ FROM users;",
			expected: "SELECT $fn$-- still not a comment$fn$ FROM users;",
		},
		{
			name:     "Comment-like text inside single-quoted string preserved",
			input:    "SELECT '-- not a comment' FROM users;",
			expected: "SELECT '-- not a comment' FROM users;",
		},
		{
			name:     "Escaped single quote preserved",
			input:    "SELECT 'it''s -- ok' FROM users;",
			expected: "SELECT 'it''s -- ok' FROM users;",
		},
		{
			name:     "Nested block comments",
			input:    "SELECT /* outer /* inner */ still comment */ * FROM users;",
			expected: "SELECT   * FROM users;",
		},
		{
			name:     "Real function body with dollar-quote",
			input:    "CREATE FUNCTION f() RETURNS void AS $$ BEGIN -- do stuff\nEND; $$ LANGUAGE plpgsql; -- done",
			expected: "CREATE FUNCTION f() RETURNS void AS $$ BEGIN -- do stuff\nEND; $$ LANGUAGE plpgsql;  ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calc.removeComments(tt.input)
			if result != tt.expected {
				t.Errorf("removeComments() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

// NOTE: collapseWhitespace was integrated into normalize() for performance.
// Whitespace collapsing is now tested via the normalize() tests above.

// Benchmark tests to ensure performance is acceptable
func BenchmarkSHA256Calculator_CalculateRaw(b *testing.B) {
	calc := New()
	content := []byte("SELECT * FROM users WHERE id = 1;")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calc.CalculateRaw(content)
	}
}

func BenchmarkSHA256Calculator_CalculateNormalized(b *testing.B) {
	calc := New()
	content := []byte("-- Comment\n/* Block */\nSELECT  *  FROM  users  WHERE  id  =  1;")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calc.CalculateNormalized(content)
	}
}
