package docs_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The sweep is finished — every prose file is clean, so any unguarded loop
// fails immediately. Kept (empty) rather than deleted: adding an entry with a
// reason is how a doc that genuinely wants the unguarded shape opts out, and
// the test's own arms keep such an entry honest.
var planViewSweepPending = map[string]int{}

// pgmi_plan_view carries every loaded file, so an execute loop filtered only by
// directory runs a stale editor backup as a migration and still exits 0 --
// proven live against the lock-safe example before it was fixed. internal/ai
// enforces this for embedded content, internal/docs for examples/; this covers
// the prose corpus.
func TestDocsPlanViewLoopsFilterSQLFiles(t *testing.T) {
	// AGENTS.md and CLAUDE.md are gitignored (.gitignore:69-70), so they are
	// absent on a fresh clone and in CI. Scan them when present — they are the
	// guidance an agent reads in a working copy — but never require them, or
	// this test fails everywhere the repo is checked out clean.
	roots := []string{"../../docs", "../../AGENTS.md", "../../CLAUDE.md"}

	found := map[string][]string{}

	scan := func(path string) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(body)

		rel, err := filepath.Rel("../..", path)
		if err != nil {
			rel = path
		}

		for _, m := range planViewLoop.FindAllStringIndex(text, -1) {
			q := text[m[0]:m[1]]
			if !strings.Contains(q, "content") || exactPathFilter.MatchString(q) {
				continue
			}
			if strings.Contains(q, "is_sql_file") {
				continue
			}
			found[rel] = append(found[rel], strconv.Itoa(strings.Count(text[:m[0]], "\n")+1))
		}
	}

	for _, root := range roots {
		info, err := os.Stat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", root, err)
		}
		if !info.IsDir() {
			scan(root)
			continue
		}
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
				return err
			}
			scan(path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	for file, lines := range found {
		want, pending := planViewSweepPending[file]
		switch {
		case !pending:
			t.Errorf("%s has %d unguarded pgmi_plan_view execute loop(s) at line(s) %s.\n"+
				"Join pg_temp.pgmi_source_view and filter on is_sql_file.",
				file, len(lines), strings.Join(lines, ", "))
		case len(lines) > want:
			t.Errorf("%s grew from %d to %d unguarded loops (lines %s) — the sweep only shrinks",
				file, want, len(lines), strings.Join(lines, ", "))
		case len(lines) < want:
			t.Errorf("%s is down to %d unguarded loops from %d — update planViewSweepPending "+
				"(or drop the entry if it is now clean)", file, len(lines), want)
		}
	}

	for file := range planViewSweepPending {
		if _, ok := found[file]; !ok {
			t.Errorf("%s is listed as pending but is clean — remove it from planViewSweepPending", file)
		}
	}
}
