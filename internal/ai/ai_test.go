package ai

import (
	"strings"
	"testing"
)

func TestGetOverview(t *testing.T) {
	content, err := GetOverview()
	if err != nil {
		t.Fatalf("GetOverview failed: %v", err)
	}

	if content == "" {
		t.Error("Overview content is empty")
	}

	// Verify key sections exist
	expected := []string{
		"pgmi",
		"PostgreSQL",
		"deploy.sql",
		"pgmi_source",
	}

	for _, s := range expected {
		if !strings.Contains(content, s) {
			t.Errorf("Overview missing expected content: %s", s)
		}
	}
}

func TestListSkills(t *testing.T) {
	skills, err := ListSkills()
	if err != nil {
		t.Fatalf("ListSkills failed: %v", err)
	}

	if len(skills) == 0 {
		t.Error("No skills found")
	}

	// Verify essential skills are present
	essentialSkills := []string{
		"pgmi-sql",
		"pgmi-philosophy",
	}

	skillNames := make(map[string]bool)
	for _, s := range skills {
		skillNames[s.Name] = true
	}

	for _, name := range essentialSkills {
		if !skillNames[name] {
			t.Errorf("Missing essential skill: %s", name)
		}
	}
}

func TestGetSkill(t *testing.T) {
	content, err := GetSkill("pgmi-sql")
	if err != nil {
		t.Fatalf("GetSkill failed: %v", err)
	}

	if content == "" {
		t.Error("Skill content is empty")
	}

	// Verify frontmatter is present
	if !strings.HasPrefix(content, "---") {
		t.Error("Skill missing YAML frontmatter")
	}
}

func TestGetSkillNotFound(t *testing.T) {
	_, err := GetSkill("nonexistent-skill")
	if err == nil {
		t.Error("Expected error for nonexistent skill")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Error should mention 'not found', got: %v", err)
	}
}

func TestParseSkillFrontmatter(t *testing.T) {
	content := `---
name: test-skill
description: "Test description"
scope: advanced-template
user_invocable: true
---

## Content here
`
	info := parseSkillFrontmatter(content, "test.md")

	if info.Name != "test-skill" {
		t.Errorf("Expected name 'test-skill', got '%s'", info.Name)
	}

	if info.Description != "Test description" {
		t.Errorf("Expected description 'Test description', got '%s'", info.Description)
	}

	if info.Scope != "advanced-template" {
		t.Errorf("Expected scope 'advanced-template', got '%s'", info.Scope)
	}
}

func TestAllSkillsHaveScope(t *testing.T) {
	skills, err := ListSkills()
	if err != nil {
		t.Fatalf("ListSkills failed: %v", err)
	}

	validScopes := map[string]bool{
		"core":              true,
		"advanced-template": true,
		"contributor":       true,
	}

	for _, s := range skills {
		if s.Scope == "" {
			t.Errorf("Skill %q has no scope set", s.Name)
		} else if !validScopes[s.Scope] {
			t.Errorf("Skill %q has invalid scope %q", s.Name, s.Scope)
		}
	}
}

func TestParseSkillFrontmatterNoFrontmatter(t *testing.T) {
	content := `# Just markdown content
No frontmatter here.
`
	info := parseSkillFrontmatter(content, "fallback-name.md")

	// Should fall back to filename
	if info.Name != "fallback-name" {
		t.Errorf("Expected fallback name 'fallback-name', got '%s'", info.Name)
	}
}

func TestGetClientDoctrine(t *testing.T) {
	content, err := GetClientDoctrine()
	if err != nil {
		t.Fatalf("GetClientDoctrine: %v", err)
	}
	for _, want := range []string{"/openapi.json", "Anti-Copy Directive", "DO NOT copy"} {
		if !strings.Contains(content, want) {
			t.Errorf("doctrine missing %q", want)
		}
	}
}

func TestGetClientIdiom_SeededLangs(t *testing.T) {
	for _, lang := range SupportedClientLangs {
		t.Run(lang, func(t *testing.T) {
			content, err := GetClientIdiom(lang)
			if err != nil {
				t.Fatalf("GetClientIdiom(%s): %v", lang, err)
			}
			if content == "" {
				t.Fatalf("GetClientIdiom(%s) returned empty", lang)
			}
			for _, want := range []string{"/openapi.json", "Anti-Copy Directive", "DO NOT copy"} {
				if !strings.Contains(content, want) {
					t.Errorf("%s idiom missing %q", lang, want)
				}
			}
		})
	}
}

func TestGetClientIdiom_Unknown(t *testing.T) {
	content, err := GetClientIdiom("brainfuck")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" {
		t.Error("expected empty string for unknown language")
	}
}

// The overview is what an agent reads before it ever runs `pgmi ai skills`;
// a skill missing from its table is a skill that does not exist.
func TestOverviewSkillTableMatchesEmbeddedSkills(t *testing.T) {
	overview, err := GetOverview()
	if err != nil {
		t.Fatalf("GetOverview failed: %v", err)
	}

	section := overview
	start := strings.Index(section, "## Available Skills")
	if start == -1 {
		t.Fatal(`overview has no "## Available Skills" section`)
	}
	section = section[start+len("## Available Skills"):]
	if end := strings.Index(section, "\n## "); end != -1 {
		section = section[:end]
	}

	listed := make(map[string]bool)
	for line := range strings.SplitSeq(section, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "| `") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "| `"), "`")
		listed[name] = true
	}

	names, err := GetSkillNames()
	if err != nil {
		t.Fatalf("GetSkillNames failed: %v", err)
	}

	embedded := make(map[string]bool, len(names))
	for _, name := range names {
		embedded[name] = true
		if !listed[name] {
			t.Errorf("skill %q is embedded but absent from the overview's Available Skills table", name)
		}
	}
	for name := range listed {
		if !embedded[name] {
			t.Errorf("overview lists skill %q, which `pgmi ai skill %s` cannot resolve", name, name)
		}
	}
}

func TestGetSkillNames(t *testing.T) {
	names, err := GetSkillNames()
	if err != nil {
		t.Fatalf("GetSkillNames failed: %v", err)
	}

	if len(names) == 0 {
		t.Error("No skill names returned")
	}

	// Verify names are sorted
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Skills not sorted: %s > %s", names[i-1], names[i])
		}
	}
}
