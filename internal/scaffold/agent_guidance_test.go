package scaffold

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

var (
	skillNamePattern   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	markdownLinkTarget = regexp.MustCompile(`\]\(([^)#\s]+)`)
)

const (
	maxSkillNameLen      = 64
	maxDescriptionLen    = 1024
	maxCompatibilityLen  = 500
	maxSkillBodyLines    = 500
	referencesDir        = "references"
	scaffoldedSkillsRoot = ".agents/skills"
)

// validateSkill checks one skill directory against the Agent Skills
// specification (agentskills.io/specification, re-read 2026-10-31) plus two
// rules the spec only recommends and skills-ref does not enforce: a SKILL.md
// body under 500 lines, and references one level deep.
func validateSkill(fsys fs.FS, dir string) []string {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, dir+": "+fmt.Sprintf(format, args...))
	}

	raw, err := fs.ReadFile(fsys, path.Join(dir, "SKILL.md"))
	if err != nil {
		return []string{dir + ": no SKILL.md"}
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return []string{dir + ": SKILL.md must start with YAML frontmatter"}
	}
	front, body, ok := strings.Cut(text[len("---\n"):], "\n---\n")
	if !ok {
		return []string{dir + ": SKILL.md frontmatter is not closed"}
	}

	var meta struct {
		Name          string `yaml:"name"`
		Description   string `yaml:"description"`
		Compatibility string `yaml:"compatibility"`
	}
	if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
		return []string{dir + ": frontmatter: " + err.Error()}
	}

	switch {
	case meta.Name == "":
		fail("name is required")
	case len(meta.Name) > maxSkillNameLen:
		fail("name %q is longer than %d characters", meta.Name, maxSkillNameLen)
	case !skillNamePattern.MatchString(meta.Name):
		fail("name %q: lowercase letters, digits and single inner hyphens only", meta.Name)
	case meta.Name != path.Base(dir):
		fail("name %q must equal the directory name %q", meta.Name, path.Base(dir))
	}
	if d := strings.TrimSpace(meta.Description); d == "" || len(d) > maxDescriptionLen {
		fail("description must be 1-%d characters, has %d", maxDescriptionLen, len(d))
	}
	if len(meta.Compatibility) > maxCompatibilityLen {
		fail("compatibility is longer than %d characters", maxCompatibilityLen)
	}
	if n := strings.Count(strings.TrimRight(body, "\n"), "\n") + 1; n > maxSkillBodyLines {
		fail("SKILL.md body has %d lines, the limit is %d", n, maxSkillBodyLines)
	}

	for _, m := range markdownLinkTarget.FindAllStringSubmatch(body, -1) {
		target := m[1]
		if strings.Contains(target, "://") {
			continue
		}
		if _, err := fs.Stat(fsys, path.Join(dir, target)); err != nil {
			fail("links to %s, which does not exist", target)
		}
	}

	refs, err := fs.ReadDir(fsys, path.Join(dir, referencesDir))
	if err == nil {
		for _, ref := range refs {
			if ref.IsDir() {
				fail("references/%s is a directory: keep references one level deep", ref.Name())
				continue
			}
			content, _ := fs.ReadFile(fsys, path.Join(dir, referencesDir, ref.Name()))
			for _, m := range markdownLinkTarget.FindAllStringSubmatch(string(content), -1) {
				if strings.HasSuffix(m[1], ".md") && !strings.Contains(m[1], "://") {
					fail("references/%s links to %s: a reference must not chain to another file", ref.Name(), m[1])
				}
			}
		}
	}
	return problems
}

func TestValidateSkill(t *testing.T) {
	skill := func(front, body string) string { return "---\n" + front + "\n---\n" + body }
	good := skill("name: good-skill\ndescription: Does a thing. Use when the thing is needed.", "See [ref](references/one.md).\n")

	tests := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{"valid", fstest.MapFS{
			"good-skill/SKILL.md":          {Data: []byte(good)},
			"good-skill/references/one.md": {Data: []byte("plain reference\n")},
		}, ""},
		{"no frontmatter", fstest.MapFS{"good-skill/SKILL.md": {Data: []byte("# just a body\n")}}, "must start with YAML frontmatter"},
		{"name differs from directory", fstest.MapFS{"good-skill/SKILL.md": {Data: []byte(skill("name: other\ndescription: x", ""))}}, "must equal the directory name"},
		{"uppercase name", fstest.MapFS{"Bad/SKILL.md": {Data: []byte(skill("name: Bad\ndescription: x", ""))}}, "lowercase letters"},
		{"consecutive hyphens", fstest.MapFS{"a--b/SKILL.md": {Data: []byte(skill("name: a--b\ndescription: x", ""))}}, "lowercase letters"},
		{"trailing hyphen", fstest.MapFS{"ab-/SKILL.md": {Data: []byte(skill("name: ab-\ndescription: x", ""))}}, "lowercase letters"},
		{"name too long", fstest.MapFS{strings.Repeat("a", 65) + "/SKILL.md": {Data: []byte(skill("name: "+strings.Repeat("a", 65)+"\ndescription: x", ""))}}, "longer than 64"},
		{"empty description", fstest.MapFS{"good-skill/SKILL.md": {Data: []byte(skill("name: good-skill\ndescription: \"\"", ""))}}, "description must be"},
		{"description too long", fstest.MapFS{"good-skill/SKILL.md": {Data: []byte(skill("name: good-skill\ndescription: "+strings.Repeat("d", 1025), ""))}}, "description must be"},
		{"body too long", fstest.MapFS{"good-skill/SKILL.md": {Data: []byte(skill("name: good-skill\ndescription: x", strings.Repeat("line\n", 501)))}}, "the limit is 500"},
		{"broken link", fstest.MapFS{"good-skill/SKILL.md": {Data: []byte(skill("name: good-skill\ndescription: x", "[gone](references/gone.md)\n"))}}, "does not exist"},
		{"nested references", fstest.MapFS{
			"good-skill/SKILL.md":                {Data: []byte(skill("name: good-skill\ndescription: x", ""))},
			"good-skill/references/deep/more.md": {Data: []byte("x")},
		}, "one level deep"},
		{"reference chain", fstest.MapFS{
			"good-skill/SKILL.md":          {Data: []byte(good)},
			"good-skill/references/one.md": {Data: []byte("see [two](two.md)\n")},
			"good-skill/references/two.md": {Data: []byte("x")},
		}, "must not chain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dir string
			for p := range tt.files {
				dir = strings.SplitN(p, "/", 2)[0]
			}
			problems := strings.Join(validateSkill(tt.files, dir), "\n")
			if tt.want == "" && problems != "" {
				t.Fatalf("valid skill rejected:\n%s", problems)
			}
			if tt.want != "" && !strings.Contains(problems, tt.want) {
				t.Fatalf("want a problem containing %q, got:\n%s", tt.want, problems)
			}
		})
	}
}

func scaffoldInto(t *testing.T, template string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), template)
	if err := NewScaffolder(false).CreateProject("proj", template, dir); err != nil {
		t.Fatalf("scaffold %s: %v", template, err)
	}
	return dir
}

func scaffoldedSkills(t *testing.T, projectDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(projectDir, scaffoldedSkillsRoot))
	if err != nil {
		t.Fatalf("read %s: %v", scaffoldedSkillsRoot, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestScaffoldWritesAgentGuidance(t *testing.T) {
	for _, template := range []string{"basic", "advanced"} {
		t.Run(template, func(t *testing.T) {
			dir := scaffoldInto(t, template)

			claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
			if err != nil {
				t.Fatalf("CLAUDE.md: %v", err)
			}
			if strings.TrimSpace(string(claude)) != "@AGENTS.md" {
				t.Errorf("CLAUDE.md must import AGENTS.md and nothing else, got %q", claude)
			}

			agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatalf("AGENTS.md: %v", err)
			}

			skills := scaffoldedSkills(t, dir)
			if len(skills) == 0 {
				t.Fatal("no skills scaffolded")
			}
			for _, name := range skills {
				if problems := validateSkill(os.DirFS(filepath.Join(dir, scaffoldedSkillsRoot)), name); len(problems) > 0 {
					t.Errorf("skill fails the spec gate:\n  %s", strings.Join(problems, "\n  "))
				}
				if !bytes.Contains(agents, []byte(scaffoldedSkillsRoot+"/"+name+"/")) {
					t.Errorf("AGENTS.md does not route to %s/%s/", scaffoldedSkillsRoot, name)
				}
			}
		})
	}
}

func sharedSkillNames(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(sharedSkillsFS, sharedSkillsDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestBasicScaffoldGetsExactlyTheSharedSkills(t *testing.T) {
	shared := sharedSkillNames(t)
	if got := scaffoldedSkills(t, scaffoldInto(t, "basic")); !slices.Equal(got, shared) {
		t.Errorf("basic scaffold skills = %v, want exactly the shared skills %v", got, shared)
	}
	advanced := scaffoldedSkills(t, scaffoldInto(t, "advanced"))
	for _, name := range shared {
		if !slices.Contains(advanced, name) {
			t.Errorf("advanced scaffold lacks shared skill %s", name)
		}
	}
}

// A shared skill reaches basic projects, which have none of the advanced
// template's schemas or protocols.
func TestSharedSkillsNameNoAdvancedOnlyIdentifiers(t *testing.T) {
	advancedOnly := regexp.MustCompile(`\bapi\.|\bmembership\.|\binputSchema\b|\bgateway\b`)
	err := fs.WalkDir(sharedSkillsFS, sharedSkillsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := fs.ReadFile(sharedSkillsFS, p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(content), "\n") {
			if m := advancedOnly.FindString(line); m != "" {
				t.Errorf("%s:%d names advanced-only %q", p, i+1, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// AGENTS.md points at the binary for facts that change with it; a copy here
// would go stale on the next upgrade.
func TestAgentsMDPointsInsteadOfRestating(t *testing.T) {
	exitCodeRow := regexp.MustCompile(`(?m)^\|\s*1[0-6]\s*\|`)
	viewColumns := regexp.MustCompile(`is_sql_file|pgmi_checksum|execution_order|sort_key`)
	for _, template := range []string{"basic", "advanced"} {
		t.Run(template, func(t *testing.T) {
			agents, err := templatesFS.ReadFile("templates/" + template + "/AGENTS.md")
			if err != nil {
				t.Fatal(err)
			}
			text := string(agents)
			if !strings.Contains(text, "pgmi ai contract") {
				t.Error("AGENTS.md must send the agent to `pgmi ai contract` for the session API")
			}
			if exitCodeRow.MatchString(text) {
				t.Error("AGENTS.md carries an exit-code table; point to `pgmi ai` instead")
			}
			if m := viewColumns.FindString(text); m != "" {
				t.Errorf("AGENTS.md names view column %q; point to `pgmi ai contract` instead", m)
			}
		})
	}
}

// An ignore rule once hid every template's .agents/ tree, so a fresh clone
// built a binary without the skills. Every AI file the scaffold ships must be
// tracked by git and present in the embedded filesystem.
func TestScaffoldAIFilesAreTrackedAndEmbedded(t *testing.T) {
	var files []string
	for _, template := range []string{"basic", "advanced"} {
		files = append(files,
			"templates/"+template+"/AGENTS.md",
			"templates/"+template+"/CLAUDE.md")
	}
	_ = fs.WalkDir(templatesFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(p, "/"+scaffoldedSkillsRoot+"/") {
			files = append(files, p)
		}
		return nil
	})
	_ = fs.WalkDir(sharedSkillsFS, sharedSkillsDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})

	for _, f := range files {
		if _, err := fs.Stat(templatesFS, f); err != nil {
			if _, err := fs.Stat(sharedSkillsFS, f); err != nil {
				t.Errorf("%s is not in the embedded filesystem", f)
			}
		}
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; embed check done, ignore check skipped")
	}
	for _, f := range files {
		if err := exec.Command("git", "check-ignore", "-q", f).Run(); err == nil {
			t.Errorf("%s is ignored by git: a fresh clone would build a binary without it", f)
		}
	}
}
