package ai

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	sqlFence = regexp.MustCompile("(?s)```sql\n(.*?)```")

	// CALL pgmi_test() expands to top-level SAVEPOINT, which PostgreSQL refuses
	// outside an explicit transaction block (25P01).
	pgmiTestCall  = regexp.MustCompile(`(?i)CALL\s+(?:pg_temp\.)?pgmi_test\s*\(`)
	topLevelBegin = regexp.MustCompile(`(?im)^\s*BEGIN\s*;`)

	// A missing JSON key yields NULL, and NULL <> 'x' is NULL, so an assertion
	// written that way never fires and a broken endpoint deploys green.
	nullBlindJSONCompare = regexp.MustCompile(`(?s)IF\s[^;]*?->>\s*'[^']*'\)?\s*(?:<>|!=)[^;]*?THEN\s+RAISE\s+EXCEPTION`)

	// Swallowing every error with a WARNING commits a partial deploy that exits 0.
	swallowedError = regexp.MustCompile(`(?is)WHEN\s+OTHERS\s+THEN\s+RAISE\s+WARNING[^;]*;\s*(?:--[^\n]*\n\s*)*END`)
)

// TestSkillSQLSnippetsDoNotTeachSilentFailures scans the SQL an agent copies
// out of the embedded skills for three shapes that fail or, worse, pass when
// they should fail (PGMI-377).
func TestSkillSQLSnippetsDoNotTeachSilentFailures(t *testing.T) {
	var offenders []string

	err := fs.WalkDir(contentFS, "content", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		body, err := fs.ReadFile(contentFS, path)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(body), "\r\n", "\n")

		for _, m := range sqlFence.FindAllStringSubmatchIndex(text, -1) {
			block := text[m[2]:m[3]]
			at := func(off int, why string) {
				line := strings.Count(text[:m[2]+off], "\n") + 1
				offenders = append(offenders, path+":"+strconv.Itoa(line)+"  "+why)
			}

			if call := pgmiTestCall.FindStringIndex(block); call != nil && !topLevelBegin.MatchString(block[:call[0]]) {
				at(call[0], "CALL pgmi_test() without a preceding top-level BEGIN; (fails with 25P01)")
			}
			for _, c := range nullBlindJSONCompare.FindAllStringIndex(block, -1) {
				at(c[0], "->> compared with <>/!=: a missing key is NULL and never fails; use IS DISTINCT FROM")
			}
			for _, s := range swallowedError.FindAllStringIndex(block, -1) {
				at(s[0], "WHEN OTHERS THEN RAISE WARNING without re-raise: a failed file commits and exits 0")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded content: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("%d skill snippet(s) teach a pattern that fails or passes silently:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
