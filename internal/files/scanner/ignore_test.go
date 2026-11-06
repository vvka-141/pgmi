package scanner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// excludedLikeTheScanner applies the scanner's walk rule: a path is excluded
// when it matches, or when any parent directory does, because the scanner
// never walks into an excluded directory.
func excludedLikeTheScanner(m *ignoreMatcher, relPath string, isDir bool) bool {
	parts := strings.Split(relPath, "/")
	for i := 1; i < len(parts); i++ {
		if m.Match(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return m.Match(relPath, isDir)
}

var ignoreCases = []struct {
	name     string
	patterns string
	// paths ending in "/" are directories
	paths []string
}{
	{"directory pattern at any depth", "clients/\n", []string{"clients/", "clients/a.cs", "x/clients/", "x/clients/b.go", "clients.txt"}},
	{"negation", "*.tmp\n!keep.tmp\n", []string{"a.tmp", "keep.tmp", "d/keep.tmp", "d/x.tmp", "a.tmpx"}},
	{"anchored by an inner slash", "docs/*.md\n", []string{"docs/a.md", "docs/sub/b.md", "other/docs/a.md", "a.md"}},
	{"anchored by a leading slash", "/build\n", []string{"build/", "build/x.o", "src/build/", "src/build/y.o"}},
	{"leading double star", "**/logs\n", []string{"logs/", "a/logs/", "a/b/logs/x.log", "logs.txt"}},
	{"inner double star", "data/**/*.csv\n", []string{"data/a.csv", "data/x/y/b.csv", "other/data/a.csv", "data/a.txt"}},
	{"trailing double star", "foo/**\n", []string{"foo/", "foo/a", "foo/b/c", "foobar"}},
	{"trailing double star leaves the directory open for re-inclusion", "foo/**\n!foo/keep\n", []string{"foo/keep", "foo/other"}},
	{"slash double star slash", "a/**/b\n", []string{"a/b", "a/x/b", "a/x/y/b", "b", "a/bb"}},
	{"single-character wildcards and classes", "file?.sql\n[ab].txt\n[!c].md\n", []string{"file1.sql", "file10.sql", "a.txt", "c.txt", "d.md", "c.md"}},
	{"star does not cross a slash", "src/*.sql\n", []string{"src/a.sql", "src/d/b.sql", "x/src/a.sql"}},
	{"escaped hash and bang", "\\#hash\n\\!bang\n", []string{"#hash", "!bang", "hash", "bang"}},
	{"no re-include inside an excluded directory", "dir/\n!dir/keep\n", []string{"dir/keep", "dir/other", "keep"}},
	{"trailing spaces are dropped", "trailing   \n", []string{"trailing", "trailing   x"}},
	{"comments and blank lines", "# comment\n\n  \nreal\n", []string{"real", "# comment", "comment"}},
	{"everything but SQL", "*\n!*/\n!*.sql\n", []string{"a.sql", "a.txt", "d/b.sql", "d/b.txt"}},
	{"later rule wins", "!a.log\n*.log\n", []string{"a.log", "b.log"}},
}

// The matcher must agree with git on every case: .pgmiignore promises
// .gitignore syntax, so git itself is the specification.
func TestIgnoreMatcherAgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, tc := range ignoreCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, out)
			}
			if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(tc.patterns), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, p := range tc.paths {
				full := filepath.Join(repo, filepath.FromSlash(strings.TrimSuffix(p, "/")))
				if strings.HasSuffix(p, "/") {
					if err := os.MkdirAll(full, 0o755); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			m, err := parseIgnore(tc.patterns)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range tc.paths {
				// Ask about a directory without its trailing slash: git then
				// stats the worktree, as it does while walking. "foo/" would be
				// answered as a path string and differ for "foo/**".
				cmd := exec.Command("git", "-C", repo, "check-ignore", "-q", "--no-index", strings.TrimSuffix(p, "/"))
				err := cmd.Run()
				var exit *exec.ExitError
				gitIgnored := err == nil
				if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
					t.Fatalf("git check-ignore %s: %v", p, err)
				}
				isDir := strings.HasSuffix(p, "/")
				ours := excludedLikeTheScanner(m, strings.TrimSuffix(p, "/"), isDir)
				if ours != gitIgnored {
					t.Errorf("%q with patterns %q: git says ignored=%v, pgmi says %v", p, tc.patterns, gitIgnored, ours)
				}
			}
		})
	}
}

// A bad character class used to panic the scanner through MustCompile, which
// under pgmi serve takes the server down; it is a configuration error now.
func TestParseIgnoreRefusesInvalidPatterns(t *testing.T) {
	for _, pattern := range []string{"[z-a].sql", "[!]", "a[]b"} {
		m, err := parseIgnore("ok.sql\n" + pattern + "\n")
		if err == nil || m != nil {
			t.Errorf("%q: want an error, got %v", pattern, err)
			continue
		}
		if !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%q: the error must name line 2, got %v", pattern, err)
		}
	}
}

// Patterns were translated byte by byte, so é.sql compiled to a regexp for
// Ã©.sql and a non-ASCII file listed in .pgmiignore was loaded anyway.
func TestParseIgnoreMatchesNonASCII(t *testing.T) {
	tests := []struct {
		pattern, path string
		dir           bool
	}{
		{"é.sql", "é.sql", false},
		{"é.sql", "sub/é.sql", false},
		{`\é.sql`, "é.sql", false},
		{"日本/", "日本", true},
		{"日本/*.sql", "日本/a.sql", false},
		{"caf?.sql", "café.sql", false},
		{"[éè].sql", "è.sql", false},
	}
	for _, tt := range tests {
		m, err := parseIgnore(tt.pattern + "\n")
		if err != nil {
			t.Fatalf("%q: %v", tt.pattern, err)
		}
		if !m.Match(tt.path, tt.dir) {
			t.Errorf("pattern %q does not match %q", tt.pattern, tt.path)
		}
	}
}
