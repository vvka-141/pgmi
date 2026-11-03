package metadata

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestCheckUniqueIDs(t *testing.T) {
	id1 := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	id2 := uuid.MustParse("22222222-2222-4222-8222-222222222222")

	t.Run("duplicate id is rejected", func(t *testing.T) {
		files := []pgmi.FileMetadata{
			{Path: "./b.sql", Metadata: &pgmi.ScriptMetadata{ID: id1}},
			{Path: "./a.sql", Metadata: &pgmi.ScriptMetadata{ID: id1}},
		}
		err := CheckUniqueIDs(files)
		if err == nil {
			t.Fatal("expected error for duplicate id, got nil")
		}
		if !errors.Is(err, pgmi.ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig (exit 10), got: %v", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, id1.String()) || !strings.Contains(msg, "./a.sql") || !strings.Contains(msg, "./b.sql") {
			t.Errorf("error should name the id and both files, got: %s", msg)
		}
	})

	t.Run("unique ids and metadata-less files pass", func(t *testing.T) {
		files := []pgmi.FileMetadata{
			{Path: "./a.sql", Metadata: &pgmi.ScriptMetadata{ID: id1}},
			{Path: "./b.sql", Metadata: &pgmi.ScriptMetadata{ID: id2}},
			{Path: "./c.sql", Metadata: nil},
			{Path: "./d.txt", Metadata: nil},
		}
		if err := CheckUniqueIDs(files); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})
}

func TestExtract_AttributeErrorsNameTheAttribute(t *testing.T) {
	for attrs, want := range map[string]string{
		`id="abc" idempotent="true"`:                                   "id attribute is not a UUID",
		`id="11111111-1111-4111-8111-111111111111" idempotent="maybe"`: "idempotent attribute must be true or false",
	} {
		_, err := Extract("/*\n<pgmi-meta "+attrs+"></pgmi-meta>\n*/\nSELECT 1;", "./a.sql")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", attrs, err, want)
		}
		if err != nil {
			assertMetadataReference(t, err.Error())
		}
	}
}

func TestValidationErrorPointsAtReachableReference(t *testing.T) {
	_, err := ExtractAndValidate("/*\n<pgmi-meta id=\"00000000-0000-0000-0000-000000000000\" idempotent=\"true\"></pgmi-meta>\n*/\nSELECT 1;", "./a.sql")
	if err == nil {
		t.Fatal("nil UUID must fail validation")
	}
	assertMetadataReference(t, err.Error())
	if _, err := os.Stat("../scaffold/skills/pgmi-metadata/SKILL.md"); err != nil {
		t.Errorf("the hint names a skill pgmi init no longer scaffolds: %v", err)
	}
}

func assertMetadataReference(t *testing.T, msg string) {
	t.Helper()
	for _, want := range []string{".agents/skills/pgmi-metadata/SKILL.md", "https://github.com/vvka-141/pgmi/blob/main/docs/METADATA.md"} {
		if !strings.Contains(msg, want) {
			t.Errorf("metadata error must point at %s: %s", want, msg)
		}
	}
	for _, gone := range []string{"pgmi ai skill", "schema.xsd"} {
		if strings.Contains(msg, gone) {
			t.Errorf("metadata error names %q, which a user cannot reach: %s", gone, msg)
		}
	}
}
