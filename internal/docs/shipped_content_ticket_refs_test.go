package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Issue keys are private context. A scaffolded project is the user's code, and
// the embedded skills are what an agent reads; neither audience can open the
// tracker, so "PGMI-214 regression cases" tells them strictly less than naming
// the cases does.
//
// Two of these were not comments at all but RAISE NOTICE text, printed to the
// console on every deploy of a project the user had just created.
//
// This covers only what reaches a user. docs/, CLAUDE.md, .claude/ and commit
// messages are repository-internal and may cite tickets freely.
var ticketRef = regexp.MustCompile(`PGMI-[0-9]+[A-Za-z]?`)

func TestShippedContentCitesNoIssueKeys(t *testing.T) {
	roots := []string{
		"../scaffold/templates", // scaffolded into the user's project
		"../ai/content",         // embedded in the binary, served by `pgmi ai`
		"../../examples",        // published as executable documentation
	}

	var offenders []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			// Both operands must be absolute: filepath.Rel cannot relate two
			// relative paths and would leave the offender reported as
			// "../scaffold/..." — not something a reader can paste.
			rel := repoRelative(t, path)
			for i, line := range strings.Split(string(body), "\n") {
				if m := ticketRef.FindString(line); m != "" {
					offenders = append(offenders, filepath.ToSlash(rel)+":"+
						strconv.Itoa(i+1)+"  "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("%d issue-key reference(s) in content that ships to users:\n  %s\n"+
			"Say what the ticket says instead — the reader cannot open it.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// repoRelative renders path relative to the repository root so a reported
// offender can be pasted straight into an editor.
func repoRelative(t *testing.T, path string) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		return filepath.ToSlash(path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
