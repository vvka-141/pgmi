package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A deploy.sql filter on a folder no project has selects nothing and still
// exits 0. A page's examples may assume any layout, but the folders they
// filter on must exist in a scaffolded template or be named in the page's
// "Example layout" note.
func TestDocsExampleFoldersExistOrAreDeclared(t *testing.T) {
	filter := regexp.MustCompile(`(?:directory\s*=|LIKE)\s*'\./([A-Za-z0-9_.-]+)/`)
	exempt := map[string]bool{"COMING-FROM.md": true, "METADATA.md": true}

	templateFolders := map[string]bool{}
	for _, tmpl := range []string{"basic", "advanced"} {
		entries, err := os.ReadDir(filepath.Join("../../shared/templates", tmpl))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() {
				templateFolders[e.Name()] = true
			}
		}
	}

	pages, err := filepath.Glob("../../docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		if exempt[filepath.Base(page)] {
			continue
		}
		body, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		note := ""
		if i := strings.Index(text, "**Example layout.**"); i >= 0 {
			note, _, _ = strings.Cut(text[i:], "\n")
		}
		for _, m := range filter.FindAllStringSubmatch(text, -1) {
			folder := m[1]
			if templateFolders[folder] || strings.Contains(note, "`"+folder+"/`") {
				continue
			}
			t.Errorf("%s filters on ./%s/, which no template has; add it to the page's **Example layout.** note or use a template folder",
				filepath.Base(page), folder)
		}
	}
}
