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
