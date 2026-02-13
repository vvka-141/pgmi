// Package ai provides AI-digestible documentation embedded in the pgmi binary.
// This enables AI coding assistants to discover and learn pgmi conventions
// by querying the CLI directly (e.g., `pgmi ai skills`).
package ai

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed all:content
var contentFS embed.FS

var (
	skillNameRe = regexp.MustCompile(`(?m)^name:\s*["']?([^"'\n]+)["']?`)
	skillDescRe = regexp.MustCompile(`(?m)^description:\s*["']?([^"'\n]+)["']?`)
)

// SkillInfo contains metadata parsed from a skill's YAML frontmatter
type SkillInfo struct {
	Name        string
	Description string
	FilePath    string
}

// GetOverview returns the main AI overview document
func GetOverview() (string, error) {
	content, err := contentFS.ReadFile("content/overview.md")
	if err != nil {
		return "", fmt.Errorf("failed to read overview: %w", err)
	}
	return string(content), nil
}

// ListSkills returns all available skills with their metadata
func ListSkills() ([]SkillInfo, error) {
	var skills []SkillInfo

	err := fs.WalkDir(contentFS, "content/skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}

		content, err := contentFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}

		info := parseSkillFrontmatter(string(content), path)
		skills = append(skills, info)
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Sort by name
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})

	return skills, nil
}

// GetSkill returns the content of a specific skill
func GetSkill(name string) (string, error) {
	// Try direct path first
	path := fmt.Sprintf("content/skills/%s.md", name)
	content, err := contentFS.ReadFile(path)
	if err == nil {
		return string(content), nil
	}

	// Try with SKILL.md suffix (for compatibility)
	path = fmt.Sprintf("content/skills/%s/SKILL.md", name)
	content, err = contentFS.ReadFile(path)
	if err == nil {
		return string(content), nil
	}

	// List available skills for error message
	skills, listErr := ListSkills()
	if listErr != nil {
		return "", fmt.Errorf("skill '%s' not found", name)
	}

	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}

	return "", fmt.Errorf("skill '%s' not found. Available skills: %s", name, strings.Join(names, ", "))
}

// GetSkillNames returns just the names of available skills
func GetSkillNames() ([]string, error) {
	skills, err := ListSkills()
	if err != nil {
		return nil, err
	}

	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return names, nil
}

// parseSkillFrontmatter extracts name and description from YAML frontmatter
func parseSkillFrontmatter(content, path string) SkillInfo {
	info := SkillInfo{
		FilePath: path,
	}

	// Default name from filename
	base := filepath.Base(path)
	info.Name = strings.TrimSuffix(base, ".md")

	// Parse YAML frontmatter
	if !strings.HasPrefix(content, "---") {
		return info
	}

	endIdx := strings.Index(content[3:], "---")
	if endIdx == -1 {
		return info
	}

	frontmatter := content[3 : endIdx+3]

	// Extract name
	if matches := skillNameRe.FindStringSubmatch(frontmatter); len(matches) > 1 {
		info.Name = strings.TrimSpace(matches[1])
	}

	// Extract description
	if matches := skillDescRe.FindStringSubmatch(frontmatter); len(matches) > 1 {
		info.Description = strings.TrimSpace(matches[1])
	}

	return info
}

// GetTemplateDoc returns AI-focused documentation for a template
func GetTemplateDoc(name string) (string, error) {
	path := fmt.Sprintf("content/templates/%s.md", name)
	content, err := contentFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("template documentation '%s' not found", name)
	}
	return string(content), nil
}

// ListTemplateDocs returns available template documentation
func ListTemplateDocs() ([]string, error) {
	entries, err := contentFS.ReadDir("content/templates")
	if err != nil {
		// No templates directory yet - return empty
		return nil, nil
	}

	var templates []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			name := strings.TrimSuffix(entry.Name(), ".md")
			templates = append(templates, name)
		}
	}
	return templates, nil
}
