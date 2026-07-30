package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Same shape as the embedded-content guard in internal/ai: a SELECT over
// pgmi_plan_view that pulls content and feeds a loop.
var (
	planViewLoop    = regexp.MustCompile(`(?s)SELECT[^;]{0,400}?pgmi_plan_view[^;]{0,400}?(?:LOOP|\)\s*$)`)
	exactPathFilter = regexp.MustCompile(`path\s*=\s*'`)
)

// execution-order-policy asserts its whole plan against a reviewed manifest
// before executing, so an unexpected file — .bak, README.md, anything — fails
// the manifest check first, with a better message than a syntax error. That
// assertion subsumes is_sql_file, and adding the join there would blur the
// lesson the example exists to teach.
var planViewGuardExempt = map[string]string{
	filepath.FromSlash("execution-order-policy/project/deploy.sql"): "asserts the full plan against a reviewed manifest",
}

// examples/ is executed by CI and copied wholesale by users, so it pays the
// same guard the embedded docs do. Verified live before this test existed:
// dropping migrations/001_orders.sql.bak into lock-safe-deploy made the deploy
// execute it as a migration and exit 0.
func TestExamplePlanViewLoopsFilterSQLFiles(t *testing.T) {
	var offenders []string

	err := filepath.WalkDir("../../examples", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}

		rel, relErr := filepath.Rel("../../examples", path)
		if relErr == nil {
			if why, ok := planViewGuardExempt[rel]; ok {
				t.Logf("skipping %s: %s", rel, why)
				return nil
			}
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)

		for _, m := range planViewLoop.FindAllStringIndex(text, -1) {
			q := text[m[0]:m[1]]
			if !strings.Contains(q, "content") || exactPathFilter.MatchString(q) {
				continue
			}
			if strings.Contains(q, "is_sql_file") {
				continue
			}
			offenders = append(offenders, rel+":"+strconv.Itoa(strings.Count(text[:m[0]], "\n")+1))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking examples: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d example pgmi_plan_view execute loop(s) omit the is_sql_file guard:\n  %s\n"+
			"pgmi_plan_view carries every loaded file, so a stale editor backup beside a "+
			"migration runs as one and the deploy still exits 0. Join pgmi_source_view and "+
			"filter on is_sql_file, or exempt the file in planViewGuardExempt with a reason.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
