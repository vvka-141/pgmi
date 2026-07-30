package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// llmsBaseURL is the deployed docs-site root; llms.txt links are absolute.
const llmsBaseURL = "https://vvka-141.github.io/pgmi/"

// TestLLMSTxtLinksResolve verifies every docs-site link in website/static/llms.txt
// points at a real page. The Hugo Book site mounts repo-root docs/ at
// /docs/<slug>/ where slug is the lowercased filename, so a renamed or deleted
// docs page silently turns an llms.txt entry into a 404 aimed at agents. This
// test is the gate that makes updating llms.txt non-optional when a page moves.
func TestLLMSTxtLinksResolve(t *testing.T) {
	root := repoRoot(t)

	raw, err := os.ReadFile(filepath.Join(root, "website", "static", "llms.txt"))
	if err != nil {
		t.Fatalf("read llms.txt: %v", err)
	}

	// Available site paths for every page under docs/ (recursive): the Hugo
	// default — docs/<relative path, lowercased, .md stripped; _index.md maps
	// to its directory> — unless the page pins an explicit `url:` in front
	// matter (used to keep historical URLs stable after a file moves into a
	// subdirectory).
	slugSrc := map[string]string{}
	docsRoot := filepath.Join(root, "docs")
	err = filepath.WalkDir(docsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return err
		}
		rel, err := filepath.Rel(docsRoot, path)
		if err != nil {
			return err
		}
		rel = strings.ToLower(filepath.ToSlash(rel))
		var slug string
		if strings.HasSuffix(rel, "/_index.md") {
			slug = strings.TrimSuffix(rel, "/_index.md")
		} else {
			slug = strings.TrimSuffix(rel, ".md")
		}
		slugSrc[slug] = path

		if pinned := frontMatterURL(t, path); pinned != "" {
			pinned = strings.Trim(pinned, "/")
			slugSrc[strings.TrimPrefix(pinned, "docs/")] = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs/: %v", err)
	}

	linkRe := regexp.MustCompile(`\]\((https?://[^)\s]+)\)`)
	matches := linkRe.FindAllStringSubmatch(string(raw), -1)
	if len(matches) == 0 {
		t.Fatal("no markdown links found in llms.txt — did the format change?")
	}

	checked := 0
	for _, m := range matches {
		url := m[1]
		if !strings.HasPrefix(url, llmsBaseURL) {
			continue // external link (GitHub, go install) — out of scope
		}
		path := strings.TrimPrefix(url, llmsBaseURL)

		anchor := ""
		if i := strings.IndexByte(path, '#'); i >= 0 {
			anchor, path = path[i+1:], path[:i]
		}
		path = strings.Trim(path, "/")
		if !strings.HasPrefix(path, "docs/") {
			continue // only doc pages have a filesystem source to check
		}
		slug := strings.TrimPrefix(path, "docs/")

		src, ok := slugSrc[slug]
		if !ok {
			t.Errorf("llms.txt links %s but no docs/*.md maps to slug %q", url, slug)
			continue
		}
		checked++
		if anchor != "" {
			if err := assertAnchor(src, anchor); err != nil {
				t.Errorf("llms.txt links %s: %v", url, err)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no docs-site links checked — base URL or llms.txt format drifted")
	}
}

// frontMatterURL returns the `url:` value from a page's YAML front matter, or
// "" when the page has none.
func frontMatterURL(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "---" {
			return ""
		}
		if v, ok := strings.CutPrefix(line, "url:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// assertAnchor confirms path has a heading whose GitHub/Hugo anchor equals frag.
func assertAnchor(path, frag string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		heading := strings.TrimLeft(line, "# ")
		if headingAnchor(heading) == frag {
			return nil
		}
	}
	return fmt.Errorf("no heading anchors to #%s in %s", frag, filepath.Base(path))
}

// headingAnchor slugifies a heading the way GitHub and Hugo do: lowercase, drop
// punctuation, map spaces and existing hyphens to '-' (consecutive separators
// are preserved, matching e.g. "--compat" -> "...---...").
func headingAnchor(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from working directory")
		}
		dir = parent
	}
}
