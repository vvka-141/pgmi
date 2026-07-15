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

	// Available slugs: lowercased filename (sans .md) of every docs/*.md.
	slugSrc := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatalf("read docs/: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		slug := strings.ToLower(strings.TrimSuffix(name, ".md"))
		slugSrc[slug] = filepath.Join(root, "docs", name)
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
