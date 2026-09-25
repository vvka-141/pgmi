package docs_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reraiseWithoutErrcode matches a WHEN OTHERS handler whose first statement
// rebuilds the error with RAISE EXCEPTION. Without USING ERRCODE that turns
// every failure into P0001, so the exit code and a retry loop can no longer tell
// a retryable 40001 from a bug, and it discards the position pgmi uses to name
// the failing line (PGMI-376).
var reraiseWithoutErrcode = regexp.MustCompile(`(?is)WHEN\s+OTHERS\s+THEN\s+(?:--[^\n]*\n\s*)*RAISE\s+EXCEPTION\s+'[^']*'[^;]*;`)

func TestDocsDoNotTeachSQLSTATEFlatteningReraise(t *testing.T) {
	roots := []string{"../../docs", "../../examples", "../../internal/ai/content", "../../internal/scaffold/templates", "../../README.md"}

	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if ext := filepath.Ext(path); ext != ".md" && ext != ".sql" {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(body)
			for _, m := range reraiseWithoutErrcode.FindAllStringIndex(text, -1) {
				if strings.Contains(strings.ToUpper(text[m[0]:m[1]]), "ERRCODE") {
					continue
				}
				t.Errorf("%s:%d re-raises with RAISE EXCEPTION and no USING ERRCODE; let the error propagate or use a bare RAISE;",
					path, strings.Count(text[:m[0]], "\n")+1)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
