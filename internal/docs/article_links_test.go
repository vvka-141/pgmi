package docs_test

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"gopkg.in/yaml.v3"
)

var articleLinkRE = regexp.MustCompile(`https://vvka-141\.github\.io/pgmi/articles/([^/#)\s"'>]+)/?(#[^)\s"'>]*)?`)

// article is what a link can land on: the URL paths it answers (its own and
// its aliases) and the anchors of its headings.
type article struct {
	paths   []string
	anchors map[string]bool
}

func TestArticleLinksResolve(t *testing.T) {
	articles := loadArticles(t, "../../website/content/articles")

	var sources []string
	sources = append(sources, "../../README.md")
	for _, pattern := range []string{"../../docs/*.md", "../../docs/*/*.md", "../../docs/*/*/*.md", "../../internal/scaffold/templates/*/README.md"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, matches...)
	}
	docs := map[string]string{}
	for _, f := range sources {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		docs[f] = string(body)
	}

	for _, problem := range articleLinkProblems(docs, articles) {
		t.Error(problem)
	}
}

// A renamed article without an alias breaks its old links; an alias of the
// old path, site-relative or page-relative, mends them; a link to a heading
// that is gone fails too.
func TestArticleLinksResolve_CatchesDeadLinks(t *testing.T) {
	doc := map[string]string{"README.md": "See [it](https://vvka-141.github.io/pgmi/articles/old-name/)."}

	renamed := map[string]article{"new-name": {paths: []string{"articles/new-name"}}}
	problems := articleLinkProblems(doc, renamed)
	if len(problems) != 1 || !strings.Contains(problems[0], "README.md") || !strings.Contains(problems[0], "articles/old-name") {
		t.Fatalf("a renamed article without an alias must fail naming the file and URL, got %q", problems)
	}

	for _, alias := range []string{"/articles/old-name", "/articles/old-name/", "old-name"} {
		withAlias := map[string]article{"new-name": {paths: articlePaths("new-name", "", []string{alias})}}
		if problems := articleLinkProblems(doc, withAlias); len(problems) != 0 {
			t.Errorf("alias %q should keep the old link working, got %q", alias, problems)
		}
	}

	anchored := map[string]string{"README.md": "[x](https://vvka-141.github.io/pgmi/articles/a/#gone-heading)"}
	withHeading := map[string]article{"a": {paths: []string{"articles/a"}, anchors: map[string]bool{"kept-heading": true}}}
	if problems := articleLinkProblems(anchored, withHeading); len(problems) != 1 || !strings.Contains(problems[0], "link to the article, not the heading") {
		t.Errorf("a link to a missing heading must fail, got %q", problems)
	}
}

func articleLinkProblems(docs map[string]string, articles map[string]article) []string {
	known := map[string]article{}
	for _, a := range articles {
		for _, p := range a.paths {
			known[p] = a
		}
	}
	var problems []string
	for _, file := range slices.Sorted(maps.Keys(docs)) {
		for _, m := range articleLinkRE.FindAllStringSubmatch(docs[file], -1) {
			a, ok := known["articles/"+m[1]]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s links to %s, which no article in website/content/articles answers; add the old path to the renamed article's aliases:", file, m[0]))
				continue
			}
			if anchor := strings.TrimPrefix(m[2], "#"); anchor != "" && !a.anchors[anchor] {
				problems = append(problems, fmt.Sprintf("%s links to %s, but the article has no heading #%s: link to the article, not the heading, or keep the heading", file, m[0], anchor))
			}
		}
	}
	return problems
}

func loadArticles(t *testing.T, dir string) map[string]article {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	articles := map[string]article{}
	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f), ".md")
		if stem == "_index" {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var fm struct {
			Slug    string   `yaml:"slug"`
			Aliases []string `yaml:"aliases"`
		}
		front, content := splitFrontMatter(body)
		if err := yaml.Unmarshal(front, &fm); err != nil {
			t.Fatalf("%s: front matter: %v", f, err)
		}
		articles[stem] = article{paths: articlePaths(stem, fm.Slug, fm.Aliases), anchors: headingAnchors(content)}
	}
	if len(articles) == 0 {
		t.Fatalf("no articles found in %s", dir)
	}
	return articles
}

// articlePaths are the site paths, without the baseURL, at which Hugo serves an
// article: its slug or file stem, and each alias. A leading slash makes an alias
// site-relative; without one it is relative to the article's section.
func articlePaths(stem, slug string, aliases []string) []string {
	name := stem
	if slug != "" {
		name = slug
	}
	paths := []string{"articles/" + name}
	for _, a := range aliases {
		if strings.HasPrefix(a, "/") {
			paths = append(paths, strings.Trim(path.Clean(a), "/"))
		} else {
			paths = append(paths, strings.Trim(path.Join("articles", a), "/"))
		}
	}
	return paths
}

func splitFrontMatter(body []byte) (front, content []byte) {
	body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(body, []byte("---\n")) {
		return nil, body
	}
	end := bytes.Index(body[4:], []byte("\n---\n"))
	if end < 0 {
		return nil, body
	}
	return body[4 : 4+end], body[4+end+5:]
}

var headingRE = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*#*\s*$`)

// headingAnchors follows Hugo's default "github" heading IDs: lower case, keep
// letters, digits, spaces, hyphens and underscores, then spaces become hyphens.
func headingAnchors(content []byte) map[string]bool {
	anchors := map[string]bool{}
	inFence := false
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := headingRE.FindStringSubmatch(line); m != nil {
			var b strings.Builder
			for _, r := range strings.ToLower(m[1]) {
				switch {
				case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
					b.WriteRune(r)
				case r == ' ':
					b.WriteRune('-')
				}
			}
			anchors[b.String()] = true
		}
	}
	return anchors
}
