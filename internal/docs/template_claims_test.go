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
	body, err := os.ReadFile("../ai/content/overview.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, _ := strings.Cut(string(body), "## Project Skills")
	section, _, _ = strings.Cut(section, "\n## ")

	want := map[string]bool{}
	for _, dir := range []string{"../scaffold/skills", "../scaffold/templates/advanced/.agents/skills"} {
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
	err = filepath.WalkDir("../scaffold/templates/advanced", func(path string, d fs.DirEntry, err error) error {
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
