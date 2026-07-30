package scaffold_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// NULL-unsafe assertions have been fixed in these templates three times. The
// shape is always the same: a test compares with <> or !=, a NULL appears on
// one side, the comparison yields NULL instead of true, the row drops out of
// the count, and the assertion passes while the thing it guards is broken.
//
// It is worst in a leak test — count the rows that should not be visible — where
// a NULL identity makes every comparison NULL and the count zero, so the test
// passes no matter what the query returns.
//
// IS DISTINCT FROM is always available and treats NULL as a difference, which
// is the fail-loud direction. Test files therefore do not use <> or != at all.
var nullUnsafeOperator = regexp.MustCompile(`<>|!=`)

// Add an entry only when the operands genuinely cannot be NULL, with the reason.
var nullUnsafeAllowed = map[string]string{}

func TestTemplateTestsAvoidNullUnsafeComparisons(t *testing.T) {
	root := "templates"

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		if !strings.Contains(filepath.ToSlash(path), "/__test__/") &&
			!strings.Contains(filepath.ToSlash(path), "/__tests__/") {
			return nil
		}

		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel := filepath.ToSlash(path)
		if why, ok := nullUnsafeAllowed[rel]; ok {
			t.Logf("skipping %s: %s", rel, why)
			return nil
		}

		for i, line := range strings.Split(string(body), "\n") {
			// A comment explaining why IS DISTINCT FROM is used is not a use.
			if code, _, _ := strings.Cut(line, "--"); nullUnsafeOperator.MatchString(code) {
				offenders = append(offenders,
					rel+":"+strconv.Itoa(i+1)+"  "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d NULL-unsafe comparison(s) in template tests:\n  %s\n"+
			"Use IS DISTINCT FROM so a NULL counts as a difference, or allow the file "+
			"in nullUnsafeAllowed with the reason its operands cannot be NULL.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
