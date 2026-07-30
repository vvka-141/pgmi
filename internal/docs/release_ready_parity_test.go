package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/release-ready.ps1 exists because make is not installed on a default
// Windows box, so `make release-ready` cannot run there at all. Two scripts
// describing one gate drift, and the failure mode is silent: the Windows
// operator runs a gate that quietly checks less than the documented one.
//
// This pins the gates themselves, not the wording. A gate added to the Makefile
// and forgotten in the script fails here.
func TestReleaseReadyParityWithMakefile(t *testing.T) {
	root := repoRoot(t)

	makefile := readFile(t, filepath.Join(root, "Makefile"))
	target := releaseReadyTarget(t, makefile)
	script := readFile(t, filepath.Join(root, "scripts", "release-ready.ps1"))

	// Each gate, and the token that proves the script runs it. The Makefile
	// reaches them through $(MAKE) lint / test-integration / test-connection,
	// so match on the target names it invokes rather than the tools.
	gates := []struct {
		name         string
		inMakeTarget string
		inPowerShell string
	}{
		{"lint", "$(MAKE) lint", "golangci-lint run"},
		{"cross-compile lint", "$(MAKE) lint", "GOOS = 'linux'"},
		{"full suite", "$(MAKE) test-integration", "go test ./... -count=1"},
		{"connection tests", "$(MAKE) test-connection", "conntest"},
		{"release notes", "release-notes.sh", "release-notes.sh"},
		{"govulncheck", "$(MAKE) vulncheck", "govulncheck"},
		{"goreleaser check", "goreleaser check", "goreleaser check"},
		{"build", "$(MAKE) build", "go build"},
	}

	for _, g := range gates {
		if !strings.Contains(target, g.inMakeTarget) {
			t.Errorf("the Makefile release-ready target no longer runs %s (looked for %q) — "+
				"if the gate moved, update this test and scripts/release-ready.ps1 together",
				g.name, g.inMakeTarget)
		}
		if !strings.Contains(script, g.inPowerShell) {
			t.Errorf("scripts/release-ready.ps1 does not run the %s gate (looked for %q); "+
				"the Makefile does, so a Windows operator would skip it", g.name, g.inPowerShell)
		}
	}

	// The "not covered here" list is a promise about what the tag workflow
	// still has to do. Both copies must make the same promise.
	for _, uncovered := range []string{
		"example gates",
		"snapshot build",
		"race detector",
		"provenance check",
	} {
		if !strings.Contains(target, uncovered) {
			t.Errorf("Makefile no longer names %q as uncovered", uncovered)
		}
		if !strings.Contains(script, uncovered) {
			t.Errorf("release-ready.ps1 does not name %q as uncovered, so it overstates what it verified", uncovered)
		}
	}

	// The example count has been wrong in two places already this release.
	if strings.Contains(script, "three end-to-end") || strings.Contains(target, "three end-to-end") {
		t.Error(`the example-gate count says "three"; examples.yml runs five`)
	}
}

func releaseReadyTarget(t *testing.T, makefile string) string {
	t.Helper()
	start := strings.Index(makefile, "\nrelease-ready:")
	if start < 0 {
		t.Fatal("Makefile has no release-ready target")
	}
	rest := makefile[start+1:]
	// A target ends at the next line that starts in column zero and is not a
	// recipe line or a continuation.
	for i, line := range strings.Split(rest, "\n") {
		if i == 0 || line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ") {
			continue
		}
		return rest[:strings.Index(rest, "\n"+line)]
	}
	return rest
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return string(b)
}
