package scanner

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func loadedPaths(r Result) []string {
	var paths []string
	for _, f := range r.Files {
		paths = append(paths, f.Path)
	}
	slices.Sort(paths)
	return paths
}

func ignoredBy(r Result) map[string]string {
	m := map[string]string{}
	for _, ig := range r.Ignored {
		m[ig.Path] = ig.Rule
	}
	return m
}

func TestPgmiignore_ExcludesWhatItNames(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("migrations/001.sql", "SELECT 1;")
	fs.AddFile("clients/csharp/App.csproj", "<Project/>")
	fs.AddFile("clients/rust/target/debug/app", "\x00binary")
	fs.AddFile("docs/notes.md", "draft")
	fs.AddFile("docs/keep.md", "keep")
	fs.AddFile("data/a/b/c.tmp", "tmp")
	fs.AddFile("data/seed.json", "{}")
	fs.AddFile(".pgmiignore", strings.Join([]string{
		"# client code is not a deployment resource",
		"clients/",
		"",
		"docs/*.md",
		"!docs/keep.md",
		"data/**/*.tmp",
	}, "\n"))

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	want := []string{"./data/seed.json", "./docs/keep.md", "./migrations/001.sql"}
	if got := loadedPaths(result); !slices.Equal(got, want) {
		t.Errorf("loaded %v, want %v", got, want)
	}

	ignored := ignoredBy(result)
	for path, rule := range map[string]string{
		"./clients/":       pgmi.IgnoreRulePgmiignore,
		"./docs/notes.md":  pgmi.IgnoreRulePgmiignore,
		"./data/a/b/c.tmp": pgmi.IgnoreRulePgmiignore,
		"./.pgmiignore":    pgmi.IgnoreRuleBuiltIn,
	} {
		if ignored[path] != rule {
			t.Errorf("%s: ignored by %q, want %q (all ignored: %v)", path, ignored[path], rule, ignored)
		}
	}
	if _, listed := ignored["./clients/rust/target/debug/app"]; listed {
		t.Error("files under an ignored directory must not be listed one by one: the directory is reported once")
	}
}

func TestPgmiignore_IgnoredDirectoryIsNotRead(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("clients/go/bin/tool", "\x00\x01 not text")
	fs.AddFile(".pgmiignore", "clients/\n")

	if _, err := s.ScanDirectory("/project"); err != nil {
		t.Fatalf("a binary file under an ignored directory must not fail the scan: %v", err)
	}
}

func TestPgmiignore_BuiltInExclusionsStillApplyAndAreReported(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("node_modules/x/index.js", "x")
	fs.AddFile(".git/HEAD", "ref")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatal(err)
	}
	ignored := ignoredBy(result)
	for _, p := range []string{"./node_modules/", "./.git/"} {
		if ignored[p] != pgmi.IgnoreRuleBuiltIn {
			t.Errorf("%s: ignored by %q, want the built-in list", p, ignored[p])
		}
	}
}

func TestPgmiignore_RefusesToIgnoreDeploySQL(t *testing.T) {
	for _, pattern := range []string{"deploy.sql", "*.sql", "*"} {
		t.Run(pattern, func(t *testing.T) {
			s, fs := newTestScanner()
			fs.AddFile("deploy.sql", "SELECT 1;")
			fs.AddFile(".pgmiignore", pattern+"\n")

			_, err := s.ScanDirectory("/project")
			if err == nil || !errors.Is(err, pgmi.ErrInvalidConfig) || !strings.Contains(err.Error(), "deploy.sql") {
				t.Fatalf("want an invalid-configuration error naming deploy.sql, got %v", err)
			}
		})
	}
}

func TestPgmiignore_ReIncludedDeploySQLIsAccepted(t *testing.T) {
	s, fs := newTestScanner()
	fs.AddFile("deploy.sql", "SELECT 1;")
	fs.AddFile("scratch.sql", "SELECT 1;")
	fs.AddFile(".pgmiignore", "*.sql\n!deploy.sql\n")

	result, err := s.ScanDirectory("/project")
	if err != nil {
		t.Fatalf("a pattern that re-includes deploy.sql must be accepted: %v", err)
	}
	if got := loadedPaths(result); len(got) != 0 {
		t.Errorf("loaded %v, want nothing (scratch.sql is ignored, deploy.sql is never loaded as a source file)", got)
	}
}
