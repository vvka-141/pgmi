package docs_test

import (
	"os"
	"regexp"
	"testing"
)

// Lint findings are version-dependent, and golangci-lint v2 rejects the repo's
// v1 .golangci.yml outright. If the Makefile and the two workflows drift apart,
// `make lint` passing locally stops meaning the release gate will pass.
func TestLintVersionPinsAgree(t *testing.T) {
	makefileRe := regexp.MustCompile(`(?m)^GOLANGCI_VERSION := (\S+)`)
	workflowRe := regexp.MustCompile(`(?m)golangci-lint-action@[^\n]*\n(?:[^\n]*\n)*?\s+version: (\S+)`)

	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(body)
	}

	m := makefileRe.FindStringSubmatch(read("../../Makefile"))
	if m == nil {
		t.Fatal("Makefile no longer declares GOLANGCI_VERSION")
	}
	want := m[1]

	for _, workflow := range []string{"../../.github/workflows/ci.yml", "../../.github/workflows/release.yml"} {
		found := workflowRe.FindAllStringSubmatch(read(workflow), -1)
		if len(found) == 0 {
			t.Errorf("%s: no golangci-lint-action version pin found", workflow)
			continue
		}
		for _, w := range found {
			if w[1] != want {
				t.Errorf("%s pins golangci-lint %s, Makefile pins %s", workflow, w[1], want)
			}
		}
	}
}
