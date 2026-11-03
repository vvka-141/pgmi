package scaffold

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	sqlFence = regexp.MustCompile("(?s)```sql[^\n]*\n(.*?)```")

	// CALL pgmi_test() expands to top-level SAVEPOINT, which PostgreSQL refuses
	// outside an explicit transaction block (25P01).
	pgmiTestCall  = regexp.MustCompile(`(?i)CALL\s+(?:pg_temp\.)?pgmi_test\s*\(`)
	topLevelBegin = regexp.MustCompile(`(?im)^\s*BEGIN\s*;`)

	// A missing JSON key yields NULL, and NULL <> 'x' is NULL, so an assertion
	// written that way never fires and a broken endpoint deploys green.
	nullBlindJSONCompare = regexp.MustCompile(`(?s)IF\s[^;]*?->>\s*'[^']*'\)?\s*(?:<>|!=)[^;]*?THEN\s+RAISE\s+EXCEPTION`)

	// Swallowing every error with a WARNING commits a partial deploy that exits 0.
	swallowedError = regexp.MustCompile(`(?is)WHEN\s+OTHERS\s+THEN\s+RAISE\s+WARNING[^;]*;\s*(?:--[^\n]*\n\s*)*END`)

	// plpgsql.check_asserts = off turns every ASSERT into a no-op.
	assertStatement = regexp.MustCompile(`(?im)^\s*ASSERT\s`)
)

// retiredAPIs are names the v0.14 template and binary removed or renamed. A
// skill that names one teaches an agent to write code that no longer deploys.
var retiredAPIs = []struct {
	term *regexp.Regexp
	why  string
}{
	{regexp.MustCompile(`requiresAuth`), "requiresAuth: a handler declares access or permissions"},
	{regexp.MustCompile(`['"](?:uri|version)['"]\s*[:,]\s*['"](?:\^|\.\*)`), "uri/version route keys: a route is a path template"},
	{regexp.MustCompile(`['"]query['"]\s*[:,]\s*(?:jsonb_build_array|\[)`), "query array: query parameters are declared in querySchema"},
	{regexp.MustCompile(`api_key`), "API keys: machine access uses approved connections"},
	{regexp.MustCompile(`(?i)x-(?:user-id|user-email|tenant-id)\b`), "identity headers: identity comes from login and organization sessions"},
	{regexp.MustCompile(`(?i)authorization:\s*bearer`), "Authorization: Bearer as identity: identity comes from login and organization sessions"},
	{regexp.MustCompile(`provision_current_user|current_user_id|current_member_org_ids|current_user_is_admin`), "identity readers: use api.current_principal_id(), current_organization_id(), has_permission()"},
	{regexp.MustCompile(`(?i)<\w+>_customer|\w+_customer\s+NOLOGIN|customer_role|customer role|→ customer`), "customer role: the roles are owner, admin, api and kernel"},
	{regexp.MustCompile(`inbound_queue`), "inbound_queue: the exchange log replaced it"},
	{regexp.MustCompile(`api\.purge_exchanges\b`), "api.purge_exchanges: use api.job_purge_exchanges or api.purge_exchanges_batch"},
	{regexp.MustCompile(`rest_route_policy_for`), "rest_route_policy_for: removed with route versioning"},
	{regexp.MustCompile(`ensure_policy|ensure_rls|has_column|vw_deployment_script`), "deploy helper removed: structure lives in run-once files"},
	{regexp.MustCompile(`pgmi_run_test_source`), "pgmi_run_test_source: the test macro runs files itself"},
	{regexp.MustCompile(`pgmi ai (?:setup|check)`), "pgmi ai setup/check: pgmi init writes the agent guidance"},
	{regexp.MustCompile(`pgmi ai (?:skills?|client)\b|\bai_skills?\b`), "pgmi ai skill/skills/client: skills live in .agents/skills/, gateways in gateways/"},
	{regexp.MustCompile(`tools/mcp-gateway`), "tools/mcp-gateway.py: the gateways live in gateways/"},
}

// connectionHandleBearer is the one bearer a skill may name: an MCP
// connection handle, which the database resolves like X-Connection.
var connectionHandleBearer = regexp.MustCompile(`(?i)authorization:\s*bearer\s+cn_`)

// TestSkillSQLSnippetsDoNotTeachSilentFailures scans the SQL an agent copies
// out of every shipped skill for three shapes that fail or, worse, pass when
// they should fail (PGMI-377).
func TestSkillSQLSnippetsDoNotTeachSilentFailures(t *testing.T) {
	if offenders := scanShippedSkills(t, silentFailures); len(offenders) > 0 {
		t.Errorf("%d skill snippet(s) teach a pattern that fails or passes silently:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// TestSkillsNameNoRetiredAPI keeps every shipped skill off the APIs this
// release removed (PGMI-453). Prose counts: an agent copies names from it too.
func TestSkillsNameNoRetiredAPI(t *testing.T) {
	if offenders := scanShippedSkills(t, retiredAPIHits); len(offenders) > 0 {
		t.Errorf("%d skill line(s) name a removed or renamed API:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// scanShippedSkills runs check over every skill pgmi ships: the shared
// scaffold skills, each template's own, and the embedded ones.
func scanShippedSkills(t *testing.T, check func(path, markdown string) []string) []string {
	t.Helper()
	var offenders []string
	scanned := 0

	scan := func(name string, fsys fs.FS, root string, keep func(string) bool) {
		err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") || !keep(path) {
				return err
			}
			body, err := fs.ReadFile(fsys, path)
			if err != nil {
				return err
			}
			scanned++
			offenders = append(offenders, check(name+"/"+path, string(body))...)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", name, err)
		}
	}
	all := func(string) bool { return true }
	scan("scaffold", sharedSkillsFS, sharedSkillsDir, all)
	scan("scaffold", templatesFS, "templates", func(p string) bool { return strings.Contains(p, "/"+scaffoldedSkillsRoot+"/") })

	if scanned == 0 {
		t.Fatal("no skill files scanned: the guard checks nothing")
	}
	return offenders
}

func retiredAPIHits(path, markdown string) []string {
	var offenders []string
	for i, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		line = connectionHandleBearer.ReplaceAllString(line, "")
		for _, r := range retiredAPIs {
			if m := r.term.FindString(line); m != "" {
				offenders = append(offenders, path+":"+strconv.Itoa(i+1)+"  "+strconv.Quote(m)+" ("+r.why+")")
			}
		}
	}
	return offenders
}

func silentFailures(path, markdown string) []string {
	var offenders []string
	text := strings.ReplaceAll(markdown, "\r\n", "\n")
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
		for _, a := range assertStatement.FindAllStringIndex(block, -1) {
			at(a[0], "ASSERT: plpgsql.check_asserts = off silences it; fail with RAISE EXCEPTION")
		}
	}
	return offenders
}

func TestSilentFailuresFlagsEachShape(t *testing.T) {
	for name, md := range map[string]string{
		"pgmi_test without BEGIN": "```sql\nCALL pgmi_test();\n```\n",
		"null-blind json compare": "```sql runnable target=__test__/t.sql\nDO $$ BEGIN IF (r->>'k') <> 'v' THEN RAISE EXCEPTION 'x'; END IF; END $$;\n```\n",
		"swallowed error":         "```sql\nBEGIN\n  PERFORM 1;\nEXCEPTION WHEN OTHERS THEN RAISE WARNING 'x';\nEND\n```\n",
		"assert statement":        "```sql\nDO $$ BEGIN\n    ASSERT (SELECT count(*) FROM t) = 5, 'five rows';\nEND $$;\n```\n",
	} {
		if got := silentFailures("skill.md", md); len(got) != 1 {
			t.Errorf("%s: want 1 offender, got %v", name, got)
		}
	}
	if got := silentFailures("skill.md", "```sql\nBEGIN;\nCALL pgmi_test();\nCOMMIT;\n```\n"); len(got) != 0 {
		t.Errorf("a pgmi_test call after BEGIN was flagged: %v", got)
	}
}

func TestRetiredAPIHitsFlagsEachTerm(t *testing.T) {
	planted := map[string]string{
		"requiresAuth":        "'requiresAuth', true",
		"uri route key":       `'uri', '^/projects/([0-9]+)$'`,
		"version route key":   `"version": ".*"`,
		"query array":         `'query', jsonb_build_array(jsonb_build_object('name', 'q'))`,
		"api key":             "membership.api_key",
		"identity header":     "send X-User-Id with the request",
		"bearer identity":     "Authorization: Bearer <token>",
		"identity reader":     "WHERE org_id = ANY(api.current_member_org_ids())",
		"customer role":       "GRANT USAGE ON SCHEMA api TO <database>_customer;",
		"inbound queue":       "api.inbound_queue_with_protocol",
		"purge procedure":     "CALL api.purge_exchanges(interval '30 days');",
		"route policy":        "api.rest_route_policy_for('GET', '/x')",
		"removed helper":      "SELECT pg_temp.has_column('core', 'task', 'title');",
		"test runner":         "pg_temp.pgmi_run_test_source('./__test__/test_a.sql')",
		"ai setup":            "run pgmi ai setup in the project",
		"ai skill":            "load it with pgmi ai skill pgmi-sql",
		"ai skills":           "list them with pgmi ai skills",
		"ai client":           "pgmi ai client typescript prints a skeleton",
		"mcp ai_skill":        "call the ai_skill tool",
		"python gateway path": "python tools/mcp-gateway.py",
	}
	for name, line := range planted {
		if got := retiredAPIHits("skill.md", "intro\n"+line+"\n"); len(got) != 1 {
			t.Errorf("%s: want 1 offender, got %v", name, got)
		}
	}
	for _, line := range []string{
		"api.job_purge_exchanges(5000) and api.purge_exchanges_batch(cutoff, 100)",
		"'uri', (request).uri",
		"{\"name\", \"version\"}",
		"internal.unloggable_headers() drops x-api-key",
		"WWW-Authenticate: Bearer realm=\"app\"",
		"send the handle as `Authorization: Bearer cn_...` or X-Connection",
		"'querySchema', jsonb_build_object('type', 'object')",
	} {
		if got := retiredAPIHits("skill.md", line); len(got) != 0 {
			t.Errorf("a current API was flagged: %v", got)
		}
	}
}
