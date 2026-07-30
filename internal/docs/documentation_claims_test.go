package docs_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHighlightsCountMatchesDocumentation(t *testing.T) {
	body, err := os.ReadFile("../../docs/HIGHLIGHTS.md")
	if err != nil {
		t.Fatalf("read highlights: %v", err)
	}

	matches := regexp.MustCompile(`(?m)^## (\d+)\.`).FindAllStringSubmatch(string(body), -1)
	if len(matches) == 0 {
		t.Fatal("docs/HIGHLIGHTS.md has no numbered capability headings")
	}
	for i, match := range matches {
		n, err := strconv.Atoi(match[1])
		if err != nil || n != i+1 {
			t.Fatalf("highlight %d is numbered %q", i+1, match[1])
		}
	}

	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"}
	if len(matches) >= len(words) {
		t.Fatalf("add a count word for %d highlights", len(matches))
	}
	// Whole word, not a substring: "ten" hides inside content, often, written,
	// extend, listen and maintenance, so a substring check passes on any English
	// page and this test could never fail.
	word := regexp.MustCompile(`(?i)\b` + words[len(matches)] + `\b`)

	for _, path := range []string{
		"../../README.md",
		"../../docs/README.md",
		"../../docs/HIGHLIGHTS.md",
		"../../docs/STYLE.md",
		"../../internal/ai/content/overview.md",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !word.Match(content) {
			t.Errorf("%s does not state the current count of %d highlights (%q)",
				path, len(matches), words[len(matches)])
		}
	}
}

func TestRollbackClaimsStayWithinPostgreSQLGuarantees(t *testing.T) {
	// Each phrase claims more than PostgreSQL delivers: sequence advances and
	// effects outside the transaction survive a rollback.
	//
	// "no side effects" is deliberately NOT here. It is how pgmi-sql.md
	// describes a pure function — the condition for an inline test rather than
	// a __test__/ one — and banning it would push that page toward worse
	// language for an unrelated reason.
	phrases := []string{
		"database is unchanged",
		"database unchanged",
		"database is untouched",
		"byte-identical",
		"zero changes to your database",
	}
	paths := []string{
		"../../README.md",
		"../../docs/QUICKSTART.md",
		"../../docs/HIGHLIGHTS.md",
		"../../docs/COMING-FROM.md",
		"../../docs/TESTING.md",
		"../../docs/session-api.md",
		"../../examples/test-gated-deploy/README.md",
		"../../internal/ai/content/overview.md",
		"../../internal/ai/content/skills/pgmi-sql.md",
		"../../internal/scaffold/templates/advanced/README.md",
		"../../internal/scaffold/templates/advanced/__test__/README.md",
	}

	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := strings.ToLower(string(body))
		for _, phrase := range phrases {
			if strings.Contains(content, phrase) {
				t.Errorf("%s contains overbroad rollback claim %q", path, phrase)
			}
		}
	}
}

func TestDocumentationGitHubYAMLParses(t *testing.T) {
	for _, path := range []string{
		"../../.github/workflows/docs.yml",
		"../../.github/ISSUE_TEMPLATE/bug.yml",
		"../../.github/ISSUE_TEMPLATE/feature.yml",
		"../../.github/ISSUE_TEMPLATE/question.yml",
		"../../.github/ISSUE_TEMPLATE/config.yml",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var document any
		if err := yaml.Unmarshal(body, &document); err != nil {
			t.Errorf("parse %s: %v", path, err)
		}
	}
}
