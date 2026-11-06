package scaffold_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
)

var (
	problemCodeLiteral = regexp.MustCompile(`(?i)\b(?:code\s*=>|problem\s*:=)\s*'([^']*)'`)
	createdFunction    = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)`)
	writerVerb         = regexp.MustCompile(`^(create|add|update|delete|complete|set)_`)
)

// namingViolations applies the two rules a skill block copied verbatim must
// already follow: problem codes are <schema>.<entity>.<reason> (public may be
// omitted, so at least one dot), and writer functions are fn_<verb>_<entity>.
func namingViolations(sql string) []string {
	var found []string
	for _, m := range problemCodeLiteral.FindAllStringSubmatch(sql, -1) {
		if !strings.Contains(m[1], ".") {
			found = append(found, "problem code '"+m[1]+"' has no dot: write <schema>.<entity>.<reason>")
		}
	}
	for _, m := range createdFunction.FindAllStringSubmatch(sql, -1) {
		if writerVerb.MatchString(strings.ToLower(m[2])) {
			found = append(found, "writer function "+m[1]+"."+m[2]+" must be named fn_"+m[2])
		}
	}
	return found
}

func TestNamingViolations(t *testing.T) {
	for sql, want := range map[string]string{
		"RETURN api.problem_response(404, 'Not Found', 'x', code => 'not_found');": "has no dot",
		"v_problem text; BEGIN problem := 'missing';":                              "has no dot",
		"CREATE OR REPLACE FUNCTION public.create_x() RETURNS void":                "fn_create_x",
		"create function core.complete_task(p uuid) returns void":                  "fn_complete_task",
		"code => 'core.task.not_found'":                                            "",
		"CREATE FUNCTION core.fn_create_task() RETURNS void":                       "",
		"CREATE FUNCTION core.task_problem(p uuid) RETURNS text":                   "",
	} {
		got := strings.Join(namingViolations(sql), "\n")
		if want == "" && got != "" {
			t.Errorf("%q: unexpected violation %s", sql, got)
		}
		if want != "" && !strings.Contains(got, want) {
			t.Errorf("%q: want a violation containing %q, got %q", sql, want, got)
		}
	}
}

func TestSkillBlocksFollowNamingRules(t *testing.T) {
	checked := 0
	for _, template := range []string{"basic", "advanced"} {
		projectDir := filepath.Join(t.TempDir(), template)
		if err := scaffold.NewScaffolder(false).CreateProject("naming", template, projectDir); err != nil {
			t.Fatal(err)
		}
		for _, b := range projectSkillBlocks(t, projectDir) {
			checked++
			for _, v := range namingViolations(b.sql) {
				t.Errorf("%s (%s): %s", b.source, template, v)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no runnable skill block found: the naming gate checks nothing")
	}
}
