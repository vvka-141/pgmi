package metadata

import (
	"errors"
	"testing"
)

// A regex ended the first comment at the first */ and could not see string
// literals, so three legal files were rejected: one whose <description>
// mentions */, and two that merely quote "<pgmi-meta" inside a literal, which
// counted as a second block. Extraction now uses the preprocessor's state
// machine, the same one that decides what a macro is.
func TestExtractIsNotFooledByLiteralsOrNesting(t *testing.T) {
	const id = "550e8400-e29b-41d4-a716-446655440000"
	block := "/*\n<pgmi-meta id=\"" + id + "\" idempotent=\"true\"></pgmi-meta>\n*/\n"

	tests := []struct {
		name    string
		content string
	}{
		{
			name: "close marker inside a description",
			content: "/*\n<pgmi-meta id=\"" + id + "\" idempotent=\"true\">\n" +
				"  <description>handles /* and */ in input</description>\n" +
				"</pgmi-meta>\n*/\nSELECT 1;",
		},
		{
			name:    "meta text quoted in a string literal",
			content: "INSERT INTO doc VALUES ('/* <pgmi-meta id=\"x\" idempotent=\"true\"/> */');\n" + block + "SELECT 1;",
		},
		{
			name:    "meta text inside a dollar-quoted body",
			content: "DO $$ BEGIN RAISE NOTICE '/* <pgmi-meta id=\"x\" idempotent=\"true\"/> */'; END $$;\n" + block,
		},
		{
			name:    "nested comment before the block",
			content: "/* banner /* inner */ still banner */\n" + block + "SELECT 1;",
		},
		{
			name:    "meta text in a quoted identifier",
			content: "CREATE TABLE \"/* <pgmi-meta id=\\\"x\\\"/> */\" (a int);\n" + block,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, err := Extract(tt.content, "./x.sql")
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if meta.ID.String() != id {
				t.Errorf("id = %s, want %s", meta.ID, id)
			}
		})
	}
}

// The multi-block rejection must still fire on two REAL blocks.
func TestExtractStillRejectsTwoRealBlocks(t *testing.T) {
	const id = "550e8400-e29b-41d4-a716-446655440000"
	one := "/*\n<pgmi-meta id=\"" + id + "\" idempotent=\"true\"></pgmi-meta>\n*/\n"

	_, err := Extract(one+"SELECT 1;\n"+one, "./x.sql")
	if err == nil {
		t.Fatal("expected an error for two metadata blocks")
	}
	if errors.Is(err, ErrNoMetadata) {
		t.Fatalf("wrong error: %v", err)
	}
	var metaErr *MetadataError
	if !errors.As(err, &metaErr) || metaErr.Message != "Multiple metadata blocks found" {
		t.Errorf("error = %v, want the multiple-blocks rejection", err)
	}
}
