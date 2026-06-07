package ai

import (
	"fmt"
	"regexp"
	"strings"
)

var skillNameRegexp = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

type ValidationError struct {
	File    string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.File, e.Message)
}

func ValidateSkillSet(files []PlannedFile) []ValidationError {
	var errs []ValidationError

	var skillFile *PlannedFile
	for i := range files {
		if strings.HasSuffix(files[i].RelPath, "/SKILL.md") {
			skillFile = &files[i]
			break
		}
	}

	if skillFile == nil {
		errs = append(errs, ValidationError{File: "SKILL.md", Message: "missing required SKILL.md"})
		return errs
	}

	parts := strings.Split(skillFile.RelPath, "/")
	if len(parts) < 2 {
		errs = append(errs, ValidationError{File: skillFile.RelPath, Message: "SKILL.md must be in a named directory"})
		return errs
	}
	dirName := parts[len(parts)-2]

	frontmatter := extractFrontmatter(skillFile.Content)

	name := frontmatterField(frontmatter, "name")
	if name == "" {
		errs = append(errs, ValidationError{File: skillFile.RelPath, Message: "frontmatter: name is required"})
	} else {
		if len(name) > 64 {
			errs = append(errs, ValidationError{File: skillFile.RelPath, Message: "frontmatter: name exceeds 64 chars"})
		}
		if !skillNameRegexp.MatchString(name) {
			errs = append(errs, ValidationError{File: skillFile.RelPath, Message: fmt.Sprintf("frontmatter: name %q must match [a-z0-9-], no leading/trailing hyphen, no consecutive --", name)})
		}
		if strings.Contains(name, "--") {
			errs = append(errs, ValidationError{File: skillFile.RelPath, Message: "frontmatter: name must not contain consecutive hyphens"})
		}
		if name != dirName {
			errs = append(errs, ValidationError{File: skillFile.RelPath, Message: fmt.Sprintf("frontmatter: name %q must equal parent directory %q", name, dirName)})
		}
	}

	desc := frontmatterField(frontmatter, "description")
	if desc == "" {
		errs = append(errs, ValidationError{File: skillFile.RelPath, Message: "frontmatter: description is required"})
	} else if len(desc) > 1024 {
		errs = append(errs, ValidationError{File: skillFile.RelPath, Message: fmt.Sprintf("frontmatter: description is %d chars (max 1024)", len(desc))})
	}

	body := stripFrontmatter(skillFile.Content)
	bodyLines := strings.Count(body, "\n")
	if bodyLines > 500 {
		errs = append(errs, ValidationError{File: skillFile.RelPath, Message: fmt.Sprintf("body is %d lines (recommended max 500)", bodyLines)})
	}

	for _, f := range files {
		if !strings.Contains(f.RelPath, "/references/") {
			continue
		}
		refParts := strings.Split(f.RelPath, "/references/")
		if len(refParts) != 2 {
			continue
		}
		if strings.Contains(refParts[1], "/") {
			errs = append(errs, ValidationError{File: f.RelPath, Message: "references must be one level deep (no subdirectories)"})
		}
	}

	return errs
}

func extractFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	end := strings.Index(content[3:], "---")
	if end == -1 {
		return ""
	}
	return content[3 : end+3]
}

func frontmatterField(fm, key string) string {
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, key+":"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---") {
		return content
	}
	end := strings.Index(content[3:], "---")
	if end == -1 {
		return content
	}
	return content[end+6:]
}
