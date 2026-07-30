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
	name    string
	pattern *regexp.Regexp
	fix     string
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
}

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
		if isTotalByConstruction(cond) {
			i = end
			continue
		}
		for _, rule := range nullUnsafeAssertions {
			if rule.pattern.MatchString(cond) {
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
