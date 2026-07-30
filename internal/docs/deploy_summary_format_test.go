package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// printDeploySummary builds every summary line as "<icon> <database>: <detail>"
// — the database name is never omitted, and the icon is ✓/✗ on a terminal and
// ok/FAILED when colour is off. Two README samples had drifted from that: one
// dropped the database name, the other capitalised "Failed" and dropped it too.
//
// Only lines that begin with an icon are checked. Prose uses ✓ and ✗ as bullets
// all over the skills, but always after a marker like "### " or "# ", never at
// the start of a line pretending to be captured output.
// Only the icon forms. The no-colour forms are "ok" and "FAILED", and "ok " at
// the start of a line is also how TAP reports a passing test — docs/TESTING.md
// shows "ok 1 - ./__test__/test_insert.sql" from the TAP reporter example.
// Matching those would flag correct TAP output, so this checks the spelling a
// terminal shows, which is the one both drifted samples used.
var (
	summaryLine   = regexp.MustCompile(`(?m)^[✓✗] `)
	summaryFormat = regexp.MustCompile(`^[✓✗] [^\s:]+: \S`)
)

func TestDocumentedDeploySummariesMatchTheRealFormat(t *testing.T) {
	roots := []string{"../../docs", "../../README.md", "../../internal/ai/content"}

	var offenders []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
				return err
			}

			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			rel, _ := filepath.Rel("../..", path)

			for i, line := range strings.Split(string(body), "\n") {
				line = strings.TrimRight(line, "\r")
				if !summaryLine.MatchString(line) || summaryFormat.MatchString(line) {
					continue
				}
				offenders = append(offenders,
					filepath.ToSlash(rel)+":"+strconv.Itoa(i+1)+"  "+line)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("%d documented deploy summary line(s) do not match "+
			`"<icon> <database>: <detail>":`+"\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
