package docs_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The pgmi ai overview names the skills pgmi init writes; it must list each
// scaffolded skill directory and nothing else.
func TestOverviewNamesTheScaffoldedSkills(t *testing.T) {
	body, err := os.ReadFile("../../shared/ai/overview.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, _ := strings.Cut(string(body), "## Project Skills")
	section, _, _ = strings.Cut(section, "\n## ")

	want := map[string]bool{}
	for _, dir := range []string{"../../shared/skills", "../../shared/templates/advanced/.agents/skills"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() {
				want[e.Name()] = true
			}
		}
	}
	named := map[string]bool{}
	for _, m := range regexp.MustCompile("`((?:pgmi|postgres)-[a-z-]+)`").FindAllStringSubmatch(section, -1) {
		named[m[1]] = true
	}
	for s := range want {
		if !named[s] {
			t.Errorf("overview.md Project Skills does not name %s", s)
		}
	}
	for s := range named {
		if !want[s] {
			t.Errorf("overview.md Project Skills names %s, which pgmi init does not write", s)
		}
	}
}

// README states the advanced template's size; the numbers follow the code.
func TestReadmeAdvancedTemplateSize(t *testing.T) {
	body, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`about ([0-9,]+) lines of SQL and ([0-9,]+) lines of tests`).FindStringSubmatch(string(body))
	if m == nil {
		t.Skip("README states no line counts")
	}
	var sql, tests int
	err = filepath.WalkDir("../../shared/templates/advanced", func(path string, d fs.DirEntry, err error) error {
		slashed := filepath.ToSlash(path)
		if err != nil || d.IsDir() || filepath.Ext(path) != ".sql" || strings.Contains(slashed, "/gateways/") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(slashed, "/__test__/") {
			tests += strings.Count(string(b), "\n")
		} else {
			sql += strings.Count(string(b), "\n")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		claim  string
		actual int
	}{{m[1], sql}, {m[2], tests}} {
		n, _ := strconv.Atoi(strings.ReplaceAll(c.claim, ",", ""))
		if diff := float64(c.actual-n) / float64(n); diff > 0.15 || diff < -0.15 {
			t.Errorf("README says about %s lines, the template has %d", c.claim, c.actual)
		}
	}
}

func TestDocsHaveNoStaleTemplateClaims(t *testing.T) {
	stale := []string{"19k lines", "~19k", "several hundred lines", "migration scaffold", "inbound queue"}
	check := func(path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range stale {
			if strings.Contains(string(b), s) {
				t.Errorf("%s still says %q", path, s)
			}
		}
	}
	check("../../README.md")
	err := filepath.WalkDir("../../docs", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" || strings.Contains(filepath.ToSlash(path), "/design/") {
			return err
		}
		check(path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Operators check these lists against their provider's allow-list before the
// first deploy, so each must name every extension advanced/deploy.sql creates.
func TestDocsNameEveryAdvancedExtension(t *testing.T) {
	deploy, err := os.ReadFile("../../shared/templates/advanced/deploy.sql")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile(`FROM \(VALUES (.*?)\) AS r\(name\)`).FindSubmatch(deploy)
	if row == nil {
		t.Fatal("no extension VALUES row found in advanced/deploy.sql")
	}
	var extensions []string
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(row[1], -1) {
		extensions = append(extensions, string(m[1]))
	}
	if len(extensions) < 5 {
		t.Fatalf("parsed only %v from the VALUES row", extensions)
	}

	places := []struct{ file, marker string }{
		{"../../docs/PRODUCTION.md", "| advanced template |"},
		{"../../docs/PRODUCTION.md", "**`CREATE EXTENSION`** for"},
		{"../../docs/templates.md", "- Extensions:"},
		{"../../shared/templates/advanced/README.md", "| `extensions` |"},
	}
	for _, p := range places {
		body, err := os.ReadFile(p.file)
		if err != nil {
			t.Fatal(err)
		}
		var line string
		for _, l := range strings.Split(string(body), "\n") {
			if strings.Contains(l, p.marker) {
				line = l
				break
			}
		}
		if line == "" {
			t.Errorf("%s: no line with %q", p.file, p.marker)
			continue
		}
		for _, ext := range extensions {
			if !strings.Contains(line, "`"+ext+"`") {
				t.Errorf("%s (%s) does not name the extension %s, which advanced/deploy.sql creates", p.file, p.marker, ext)
			}
		}
	}
}
