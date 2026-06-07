package ai

import (
	"testing"
)

func TestGeneratedSkillSetConformance(t *testing.T) {
	stamp := Stamp{Version: "1.0.0", Source: ModulePath}
	files, err := GenerateSetup("claude", stamp)
	if err != nil {
		t.Fatal(err)
	}

	errs := ValidateSkillSet(files)
	for _, e := range errs {
		t.Errorf("conformance violation: %s", e)
	}

	if len(errs) > 0 {
		t.Fatalf("%d conformance violation(s)", len(errs))
	}
}

func TestValidateSkillSet_MissingSKILL(t *testing.T) {
	files := []PlannedFile{{RelPath: "pgmi/foo.md", Content: "hello"}}
	errs := ValidateSkillSet(files)
	if len(errs) == 0 {
		t.Error("expected error for missing SKILL.md")
	}
}

func TestValidateSkillSet_BadName(t *testing.T) {
	files := []PlannedFile{{
		RelPath: "pgmi/SKILL.md",
		Content: "---\nname: INVALID\ndescription: test\n---\n# body\n",
	}}
	errs := ValidateSkillSet(files)
	if len(errs) == 0 {
		t.Error("expected error for invalid name")
	}
}

func TestValidateSkillSet_NameDirMismatch(t *testing.T) {
	files := []PlannedFile{{
		RelPath: "other/SKILL.md",
		Content: "---\nname: pgmi\ndescription: test\n---\n# body\n",
	}}
	errs := ValidateSkillSet(files)
	found := false
	for _, e := range errs {
		if e.Message != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected error for name/directory mismatch")
	}
}

func TestValidateSkillSet_NestedSubdirectory(t *testing.T) {
	files := []PlannedFile{
		{RelPath: "pgmi/SKILL.md", Content: "---\nname: pgmi\ndescription: test\n---\n# body\n"},
		{RelPath: "pgmi/sub/nested.md", Content: "x"},
	}
	errs := ValidateSkillSet(files)
	found := false
	for _, e := range errs {
		if e.File == "pgmi/sub/nested.md" {
			found = true
		}
	}
	if !found {
		t.Error("expected error for nested subdirectory")
	}
}

func TestGeneratedSkillSet_HasDepthFiles(t *testing.T) {
	stamp := Stamp{Version: "1.0.0", Source: ModulePath}
	files, err := GenerateSetup("claude", stamp)
	if err != nil {
		t.Fatal(err)
	}

	depthCount := 0
	for _, f := range files {
		if f.RelPath != "pgmi/SKILL.md" && len(f.RelPath) > 0 {
			depthCount++
		}
	}
	if depthCount == 0 {
		t.Error("expected at least one depth file alongside SKILL.md")
	}
}
