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
			expected: "d1ec24bdafb79996d598fd0707064fc6766042c1054c8ca379da5fc6a3a03e5a",
		},
		{
			name:     "SQL with comments",
			content:  "-- Comment\nSELECT * FROM users;",
			expected: "439dc674a27b169ae8ccbf89c825a4689b1faf94d5d21a95b77e77365dc9effe",
		},
		{
			name:     "Whitespace variations should differ",
			content:  "SELECT  *  FROM  users;",
			expected: "e09890a6b2a21a9fcffa0da5317f5a8872877b525570d035a38a8962cae50e53",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calc.CalculateRaw([]byte(tt.content))

			if result != tt.expected {
				t.Errorf("CalculateRaw() = %s, expected %s", result, tt.expected)
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
			name:        "Simple SQL",
			content:     "SELECT * FROM users;",
			expected:    "770ec17e2277e313a56c78ecab71d0ee460922cac72efddf9869ca4194276572",
			description: "Simple SQL should be normalized to lowercase",
		},
		{
			name:        "SQL with uppercase",
			content:     "SELECT * FROM USERS;",
			expected:    "770ec17e2277e313a56c78ecab71d0ee460922cac72efddf9869ca4194276572",
			description: "Uppercase should become lowercase",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calc.CalculateNormalized([]byte(tt.content))

			if result != tt.expected {
				t.Errorf("CalculateNormalized() = %s, expected %s", result, tt.expected)
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

// The normalized checksum is the only one pgmi_source_view exposes, so a user
// gating "has this script changed" on it must not be told two scripts are the
// same when PostgreSQL would deploy them differently.
func TestCalculateNormalized_FoldsCaseOnlyWhereSQLDoes(t *testing.T) {
	calc := New()
	same := func(a, b string) bool {
		return calc.CalculateNormalized([]byte(a)) == calc.CalculateNormalized([]byte(b))
	}

	tests := []struct {
		name string
		a, b string
		want bool
	}{
		// "Users" and "users" are two different tables.
		{"quoted identifier", `CREATE TABLE "Users" (id int);`, `CREATE TABLE "users" (id int);`, false},
		// Different seeded data, and the same shape as an ALTER ROLE password.
		{"string literal", `INSERT INTO cfg VALUES ('Production');`, `INSERT INTO cfg VALUES ('production');`, false},
		{"dollar-quoted body", `DO $$ BEGIN PERFORM 'Xy'; END $$;`, `DO $$ BEGIN PERFORM 'xy'; END $$;`, false},

		// Case is genuinely insensitive here, and staying insensitive is the
		// point of a normalized checksum.
		{"keywords and unquoted identifiers", `SELECT * FROM USERS;`, `select * from users;`, true},
		{"line endings", "SELECT 1;\r\nSELECT 2;\r\n", "SELECT 1;\nSELECT 2;\n", true},
		{"comments and indentation", "-- note\nSELECT   1;", "SELECT 1;", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := same(tt.a, tt.b); got != tt.want {
				t.Errorf("same(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
