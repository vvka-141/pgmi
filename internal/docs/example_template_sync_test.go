package docs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// examples/test-gated-deploy is a copy of the basic template's domain code,
// kept byte-identical so the README example and `pgmi init` teach the same
// SQL. Nothing enforced that, and a data-loss bug in upsert_user was fixed in
// the template while the example — which CI runs on every push — kept it.
//
// deploy.sql and README.md are deliberately NOT listed: the template's
// deploy.sql carries the migration-tracking teaching block the example omits.
// To let one of these diverge on purpose, delete its line and say why.
var exampleTemplateCopies = []string{
	"migrations/001_users.sql",
	"migrations/002_user_crud.sql",
	"__test__/_setup.sql",
	"__test__/test_user_crud.sql",
}

func TestExampleMatchesBasicTemplate(t *testing.T) {
	const (
		example  = "../../examples/test-gated-deploy"
		template = "../../internal/scaffold/templates/basic"
	)

	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// Line endings are a checkout artifact, not content.
		return strings.ReplaceAll(string(body), "\r\n", "\n")
	}

	for _, rel := range exampleTemplateCopies {
		t.Run(rel, func(t *testing.T) {
			got := read(filepath.Join(example, rel))
			want := read(filepath.Join(template, rel))
			if got != want {
				t.Errorf("examples/test-gated-deploy/%s has drifted from the basic template.\n"+
					"Port the change both ways, or drop this path from exampleTemplateCopies with a reason.", rel)
			}
		})
	}
}
