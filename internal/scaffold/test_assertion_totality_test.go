package scaffold_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
)

func repoExamplesDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for d := wd; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return filepath.Join(d, "examples")
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
	}
}

var nullUnsafeAssertions = []struct {
	name     string
	pattern  *regexp.Regexp
	fix      string
	testOnly bool
}{
	{
		name:    "!= or <> in an assertion",
		pattern: regexp.MustCompile(`(?:!=|<>)`),
		fix:     "use IS DISTINCT FROM — NULL != 'x' is NULL, not true",
	},
	{
		name:    "bare boolean operand",
		pattern: regexp.MustCompile(`^(?:NOT\s+)?[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)?$`),
		fix:     "use <expr> IS DISTINCT FROM true/false — a NULL flag takes neither branch",
	},
	{
		name:    "unguarded NOT LIKE",
		pattern: regexp.MustCompile(`(?:^|[^)\s])\s+NOT\s+LIKE`),
		fix:     "wrap the left operand in coalesce(<expr>, '') — NULL NOT LIKE 'x' is NULL",
	},
	{
		name:    "unguarded array_length",
		pattern: regexp.MustCompile(`(?:^|[^_a-z(])array_length\(`),
		fix:     "use coalesce(array_length(x, 1), 0) — an empty array yields NULL, not 0",
	},
	{
		name:     "NOT over a value that can be NULL",
		pattern:  regexp.MustCompile(`(?:^|\(|\bOR |\bAND )NOT (?:\(|[a-z_])`),
		fix:      "write (<expr>) IS NOT TRUE — NOT NULL is NULL, so the RAISE is skipped",
		testOnly: true,
	},
	{
		name:     "= in an assertion",
		pattern:  regexp.MustCompile(`[^<>!=:]=[^>=]`),
		fix:      "use IS NOT DISTINCT FROM — NULL = x is NULL, so the RAISE is skipped",
		testOnly: true,
	},
	{
		name:     "!~ in an assertion",
		pattern:  regexp.MustCompile(`!~`),
		fix:      "wrap the left operand in coalesce(<expr>, '') — NULL !~ 'x' is NULL",
		testOnly: true,
	},
}

// nullSafeAllowed exempts one assertion, by file and the start of its
// condition text, whose operands are proven never NULL. Give the reason.
var nullSafeAllowed = map[string]string{}

var (
	ifHeader   = regexp.MustCompile(`^\s*(?:IF|ELSIF)\b`)
	thenTail   = regexp.MustCompile(`\bTHEN\s*$`)
	raiseStart = regexp.MustCompile(`^\s*RAISE\s+EXCEPTION\b`)
	sqlLiteral = regexp.MustCompile(`'(?:[^']|'')*'`)
	existsOnly = regexp.MustCompile(`^(?:NOT\s+)?EXISTS\s*\(.*\)$`)
)

// TestTemplateAssertionsAreTotal fails on any assertion in a shipped SQL file
// that a missing row or absent key would silently switch off.
func TestTemplateAssertionsAreTotal(t *testing.T) {
	var offenders []string

	err := fs.WalkDir(scaffold.GetTemplatesFS(), "templates", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir(), !strings.HasSuffix(path, ".sql"):
			return err
		}

		body, readErr := fs.ReadFile(scaffold.GetTemplatesFS(), path)
		if readErr != nil {
			return readErr
		}
		offenders = append(offenders, scanAssertions(path, string(body))...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded templates: %v", err)
	}

	if exDir := repoExamplesDir(); exDir != "" {
		_ = filepath.WalkDir(exDir, func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir(), !strings.HasSuffix(path, ".sql"):
				return nil
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			rel, _ := filepath.Rel(filepath.Dir(exDir), path)
			offenders = append(offenders, scanAssertions(filepath.ToSlash(rel), string(body))...)
			return nil
		})
	}

	if len(offenders) > 0 {
		t.Errorf("%d assertion(s) evaluate to NULL when their input is missing, "+
			"so they cannot fail:\n  %s", len(offenders), strings.Join(offenders, "\n  "))
	}
}

func scanAssertions(path, body string) []string {
	var offenders []string
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")

	for i := 0; i < len(lines); i++ {
		if !ifHeader.MatchString(lines[i]) {
			continue
		}

		end := i
		for end < len(lines) && !thenTail.MatchString(strings.TrimRight(lines[end], " \t")) {
			end++
		}
		if end >= len(lines) {
			continue
		}

		// Only conditions guarding a RAISE EXCEPTION are assertions; an IF that
		// drives control flow is free to treat NULL as "skip".
		next := end + 1
		for next < len(lines) && strings.TrimSpace(lines[next]) == "" {
			next++
		}
		if next >= len(lines) || !raiseStart.MatchString(lines[next]) {
			i = end
			continue
		}

		cond := conditionText(lines[i : end+1])
		if isTotalByConstruction(cond) || allowedAssertion(path, cond) {
			i = end
			continue
		}
		shape := withoutSubqueries(cond)
		inTest := strings.Contains(path, "/__test__/") || strings.Contains(path, "/__tests__/")
		for _, rule := range nullUnsafeAssertions {
			if rule.testOnly && !inTest {
				continue
			}
			if rule.pattern.MatchString(shape) {
				offenders = append(offenders, fmt.Sprintf("%s:%d [%s] %s\n      -> %s",
					path, i+1, rule.name, truncate(cond, 110), rule.fix))
			}
		}
		i = end
	}
	return offenders
}

// isTotalByConstruction reports conditions that cannot yield NULL whatever the
// data does: EXISTS is three-valued-free, and SQLERRM/SQLSTATE are never NULL
// inside the exception handler that is the only place they are read.
func isTotalByConstruction(cond string) bool {
	return existsOnly.MatchString(cond) ||
		strings.Contains(cond, "SQLERRM") || strings.Contains(cond, "SQLSTATE") ||
		strings.Contains(cond, "try_cast(") || strings.Contains(cond, "?>")
}

func allowedAssertion(path, cond string) bool {
	for key := range nullSafeAllowed {
		file, prefix, _ := strings.Cut(key, ": ")
		if strings.HasSuffix(path, file) && strings.HasPrefix(cond, prefix) {
			return true
		}
	}
	return false
}

var (
	existsNotFound = regexp.MustCompile(`\bNOT (?:EXISTS|FOUND)\b`)
	nullTests      = regexp.MustCompile(`\bIS (?:NOT )?(?:TRUE|FALSE|NULL|DISTINCT FROM)\b|\bIS NOT DISTINCT FROM\b`)
)

// withoutSubqueries reduces a condition to the shape the rules judge: a
// parenthesised SELECT becomes (), so an = in its WHERE is not a comparison of
// the assertion; NOT EXISTS and NOT FOUND are total; and an expression already
// closed by IS [NOT] TRUE/FALSE/NULL/DISTINCT FROM keeps none of its inner
// operators, since the test makes it total.
func withoutSubqueries(cond string) string {
	out := cond
	for {
		start := strings.Index(out, "(SELECT ")
		if start < 0 {
			break
		}
		depth, end := 0, -1
		for i := start; i < len(out); i++ {
			switch out[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 {
				end = i
				break
			}
		}
		if end < 0 {
			break
		}
		out = out[:start] + "()" + out[end+1:]
	}
	out = existsNotFound.ReplaceAllString(out, "TOTAL")
	for {
		loc := nullTests.FindStringIndex(out)
		if loc == nil {
			break
		}
		left := operandStart(out[:loc[0]])
		out = out[:left] + "TOTAL " + out[loc[1]:]
	}
	return out
}

// operandStart finds where the operand of a trailing IS ... test begins: the
// matching ( of a closing ), else the start of the last word.
func operandStart(s string) int {
	s = strings.TrimRight(s, " ")
	if strings.HasSuffix(s, ")") {
		depth := 0
		for i := len(s) - 1; i >= 0; i-- {
			switch s[i] {
			case ')':
				depth++
			case '(':
				depth--
			}
			if depth == 0 {
				for i > 0 && (s[i-1] == '.' || s[i-1] == '_' || isWordByte(s[i-1])) {
					i--
				}
				return i
			}
		}
		return 0
	}
	i := len(s)
	for i > 0 && s[i-1] != ' ' && s[i-1] != '(' {
		i--
	}
	return i
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// conditionText strips the IF/THEN scaffolding, comments and string literals so
// the patterns match operators rather than prose inside a message.
func conditionText(lines []string) string {
	joined := strings.Join(lines, " ")
	if idx := strings.Index(joined, "--"); idx >= 0 {
		joined = joined[:idx]
	}
	joined = sqlLiteral.ReplaceAllString(joined, "''")
	joined = ifHeader.ReplaceAllString(joined, "")
	joined = thenTail.ReplaceAllString(strings.TrimRight(joined, " \t"), "")
	return strings.Join(strings.Fields(joined), " ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func TestScanAssertionsFlagsEachNullUnsafeShape(t *testing.T) {
	bad := map[string]string{
		"NOT over a value that can be NULL": "IF NOT v_body ? 'x' THEN",
		"= in an assertion":                 "IF v_etag_after = v_etag THEN",
		"!~ in an assertion":                "IF v_html !~ 'x' THEN",
	}
	for rule, head := range bad {
		body := "DO $$ BEGIN\n    " + head + "\n        RAISE EXCEPTION 'x';\n    END IF;\nEND $$;\n"
		got := scanAssertions("templates/advanced/lib/__test__/probe.sql", body)
		if len(got) != 1 || !strings.Contains(got[0], rule) {
			t.Errorf("%s: want one %q finding, got %v", head, rule, got)
		}
		if got := scanAssertions("templates/advanced/deploy.sql", body); len(got) != 0 {
			t.Errorf("%s outside a test file: want no finding, got %v", head, got)
		}
	}
	for _, head := range []string{
		"IF (v_body ? 'x') IS NOT TRUE THEN",
		"IF (v_etag_after = v_etag) IS NOT FALSE THEN",
		"IF NOT EXISTS (SELECT FROM t WHERE a = b) THEN",
		"IF NOT FOUND THEN",
		"IF (SELECT count(*) FROM t WHERE a = b) IS DISTINCT FROM 1 THEN",
	} {
		body := "DO $$ BEGIN\n    " + head + "\n        RAISE EXCEPTION 'x';\n    END IF;\nEND $$;\n"
		if got := scanAssertions("templates/advanced/lib/__test__/probe.sql", body); len(got) != 0 {
			t.Errorf("%s: want no finding, got %v", head, got)
		}
	}
}
