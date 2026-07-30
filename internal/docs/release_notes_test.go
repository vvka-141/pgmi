package docs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scripts/release-notes.sh is what stands between a half-finished RELEASES.md
// and a published release page. It used to check only that the section was
// non-empty, so a tag could be cut while the notes still said TBD.
func TestReleaseNotesScriptRejectsUnfinishedSections(t *testing.T) {
	// On Windows the verdict depends on which bash PATH resolves: Git Bash
	// translates the native paths below, the WSL launcher mangles them. CI runs
	// this script on ubuntu, where the answer is not ambient.
	if runtime.GOOS == "windows" {
		t.Skip("release-notes.sh is Linux release tooling; CI covers it")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH; skipping release-notes script test")
	}
	script := filepath.Join(repoRoot(t), "scripts", "release-notes.sh")

	const dated = "# Releases\n\n## v1.2.3 — 2026-07-26\n\nWhat changed, for a stranger.\n\n## v1.2.2 — 2026-01-01\n\nOlder.\n"

	tests := []struct {
		name       string
		releases   string
		tag        string
		wantPass   bool
		wantReason string
	}{
		{
			name:     "a dated, final section passes",
			releases: dated,
			tag:      "v1.2.3",
			wantPass: true,
		},
		{
			name:       "a draft marker above the heading fails",
			releases:   "# Releases\n\n<!-- DRAFT — set the real date when tagging. -->\n## v1.2.3 — 2026-07-26\n\nBody.\n",
			tag:        "v1.2.3",
			wantReason: "still marked a draft",
		},
		{
			name:       "a TBD date fails",
			releases:   "# Releases\n\n## v1.2.3 — TBD\n\nBody.\n",
			tag:        "v1.2.3",
			wantReason: "no release date",
		},
		{
			name:       "no date at all fails",
			releases:   "# Releases\n\n## v1.2.3\n\nBody.\n",
			tag:        "v1.2.3",
			wantReason: "no release date",
		},
		{
			name:       "a missing section still fails with the original message",
			releases:   dated,
			tag:        "v9.9.9",
			wantReason: "has no section",
		},
		{
			name:       "an empty section fails",
			releases:   "# Releases\n\n## v1.2.3 — 2026-07-26\n\n## v1.2.2 — 2026-01-01\n\nOlder.\n",
			tag:        "v1.2.3",
			wantReason: "has no section",
		},
		{
			name:       "a blank line between draft marker and heading still fails",
			releases:   "# Releases\n\n<!-- DRAFT -->\n\n## v1.2.3 — 2026-07-26\n\nBody.\n",
			tag:        "v1.2.3",
			wantReason: "still marked a draft",
		},
		{
			name:       "a draft marker inside the section body fails",
			releases:   "# Releases\n\n## v1.2.3 — 2026-07-26\n\n<!-- DRAFT -->\nBody.\n",
			tag:        "v1.2.3",
			wantReason: "still marked a draft",
		},
		{
			// RELEASES.md opens by explaining this very rule, and quotes the
			// marker inside backticks to do it. An unanchored match read that
			// sentence as a draft marker, so v0.12.0 could not be released even
			// with the real marker removed and the heading dated — and no
			// fixture here carried a preamble, so nothing caught it.
			name: "instructional prose quoting the marker passes",
			releases: "# Releases\n\n" +
				"> **Writing a release?** The tag build fails if the heading carries no\n" +
				"> `YYYY-MM-DD` date, or if a `<!-- DRAFT -->` marker still sits above it.\n\n" +
				"## v1.2.3 — 2026-07-26\n\nWhat changed, for a stranger.\n",
			tag:      "v1.2.3",
			wantPass: true,
		},
		{
			// The anchor allows leading whitespace, so an indented real marker
			// must not become a way to sneak a draft out.
			name:       "an indented draft marker still fails",
			releases:   "# Releases\n\n  <!-- DRAFT -->\n## v1.2.3 — 2026-07-26\n\nBody.\n",
			tag:        "v1.2.3",
			wantReason: "still marked a draft",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "RELEASES.md")
			if err := os.WriteFile(fixture, []byte(tt.releases), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			cmd := exec.Command(bash, script, tt.tag)
			cmd.Env = append(os.Environ(), "PGMI_RELEASES_FILE="+fixture)
			out, err := cmd.CombinedOutput()

			if tt.wantPass {
				if err != nil {
					t.Fatalf("expected the script to succeed, got %v\n%s", err, out)
				}
				if !strings.Contains(string(out), "What changed") {
					t.Errorf("expected the section body on stdout, got:\n%s", out)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected the script to fail, it succeeded with:\n%s", out)
			}
			if !strings.Contains(string(out), tt.wantReason) {
				t.Errorf("expected a failure mentioning %q, got:\n%s", tt.wantReason, out)
			}
		})
	}
}
