package ai

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// planViewQuery matches a SELECT ... pgmi_plan_view ... up to the end of the
// subquery or loop header — the shape an agent copies into deploy.sql.
var planViewQuery = regexp.MustCompile(`(?s)SELECT[^;]{0,400}?pgmi_plan_view[^;]{0,400}?(?:LOOP|\)\s*$)`)

// exactPathFilter marks a query that names one file outright, which cannot pick
// up a neighbouring backup and so needs no is_sql_file guard.
var exactPathFilter = regexp.MustCompile(`path\s*=\s*'`)

// TestEmbeddedPlanViewExamplesFilterSQLFiles keeps the agent-facing corpus from
// teaching a loop that executes non-SQL. pgmi_plan_view carries every loaded
// file, so an unguarded loop runs README.md, and — worse, because it fails
// silently — a stale editor backup of a migration.
func TestEmbeddedPlanViewExamplesFilterSQLFiles(t *testing.T) {
	var offenders []string

	err := fs.WalkDir(contentFS, "content", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}

		body, err := fs.ReadFile(contentFS, path)
		if err != nil {
			return err
		}
		text := string(body)

		for _, m := range planViewQuery.FindAllStringIndex(text, -1) {
			q := text[m[0]:m[1]]

			// Diagnostic listings and single-file lookups are not execute-all loops.
			if !strings.Contains(q, "content") || exactPathFilter.MatchString(q) {
				continue
			}
			if strings.Contains(q, "is_sql_file") {
				continue
			}

			line := strings.Count(text[:m[0]], "\n") + 1
			offenders = append(offenders, path+":"+strconv.Itoa(line)+"  "+condense(q))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded content: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d embedded pgmi_plan_view execute loop(s) omit the is_sql_file guard.\n"+
			"An agent copying these executes editor backups as migrations.\n"+
			"Join pg_temp.pgmi_source_view and filter on is_sql_file:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func condense(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
