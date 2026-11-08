package cli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/metadata"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func scaffoldMeta(t *testing.T, dir, sortKey string) error {
	t.Helper()
	resetMetadataFlags()
	scaffoldWrite = true
	scaffoldSortKey = sortKey
	return runMetadataScaffold(metadataScaffoldCmd, []string{dir})
}

// A path-derived key ran api/orders.sql before core/orders.sql ("api" < "core").
// The caller's prefix is what expresses the layering.
func TestMetadataScaffold_CallerPrefixOrdersLayers(t *testing.T) {
	dir := createTestProject(t, map[string]string{
		"deploy.sql":      "SELECT 1;",
		"core/orders.sql": "CREATE TABLE core_orders(id int);",
		"api/orders.sql":  "CREATE VIEW api_orders AS SELECT 1;",
	})
	if err := scaffoldMeta(t, filepath.Join(dir, "core"), "005/core/"); err != nil {
		t.Fatal(err)
	}
	if err := scaffoldMeta(t, filepath.Join(dir, "api"), "006/api"); err != nil {
		t.Fatal(err)
	}

	result, err := planProject(dir)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var order []string
	for _, e := range result.Plan {
		order = append(order, e.SortKey+" "+e.Path)
	}
	want := []string{"005/core/orders.sql ./core/orders.sql", "006/api/orders.sql ./api/orders.sql"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Errorf("plan = %v, want %v", order, want)
	}
}

func TestMetadataScaffold_DerivesPrefixFromSiblings(t *testing.T) {
	dir := createTestProject(t, map[string]string{
		"deploy.sql":     "SELECT 1;",
		"core/a.sql":     metaFile("11111111-1111-4111-8111-111111111111", "005/core/a.sql"),
		"core/b.sql":     "SELECT 2;",
		"other/x.sql":    metaFile("22222222-2222-4222-8222-222222222222", "007/x.sql"),
		"other/y.sql":    "SELECT 3;",
		"__test__/t.sql": "SELECT 4;",
	})
	if err := scaffoldMeta(t, dir, ""); err != nil {
		t.Fatal(err)
	}
	for file, key := range map[string]string{"core/b.sql": "005/core/b.sql", "other/y.sql": "007/y.sql"} {
		content, _ := os.ReadFile(filepath.Join(dir, file))
		m, err := metadata.Extract(string(content), file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if len(m.SortKeys.Keys) != 1 || m.SortKeys.Keys[0] != key {
			t.Errorf("%s keys = %v, want [%s]", file, m.SortKeys.Keys, key)
		}
	}
}

func TestMetadataScaffold_RefusesWithoutAPrefix(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no sibling metadata": {
			"deploy.sql": "SELECT 1;",
			"core/a.sql": "SELECT 1;",
		},
		"siblings disagree": {
			"deploy.sql": "SELECT 1;",
			"core/a.sql": metaFile("11111111-1111-4111-8111-111111111111", "005/core/a.sql"),
			"core/b.sql": metaFile("22222222-2222-4222-8222-222222222222", "006/core/b.sql"),
			"core/c.sql": "SELECT 3;",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := createTestProject(t, files)
			before := map[string]string{}
			for f := range files {
				b, _ := os.ReadFile(filepath.Join(dir, f))
				before[f] = string(b)
			}
			err := scaffoldMeta(t, dir, "")
			if !errors.Is(err, pgmi.ErrUsage) || !strings.Contains(err.Error(), "--sort-key") {
				t.Fatalf("err = %v, want a usage error naming --sort-key", err)
			}
			for f, want := range before {
				if b, _ := os.ReadFile(filepath.Join(dir, f)); string(b) != want {
					t.Errorf("%s modified despite the refusal", f)
				}
			}
		})
	}
}

func TestMetadataScaffold_IDsAreUniqueAcrossRuns(t *testing.T) {
	dir := createTestProject(t, map[string]string{
		"deploy.sql":      "SELECT 1;",
		"core/orders.sql": "SELECT 1;",
		"api/orders.sql":  "SELECT 2;",
	})
	if err := scaffoldMeta(t, filepath.Join(dir, "core"), "005/core/"); err != nil {
		t.Fatal(err)
	}
	if err := scaffoldMeta(t, filepath.Join(dir, "api"), "006/api/"); err != nil {
		t.Fatal(err)
	}
	result, err := validateProject(dir)
	if err != nil || !result.ValidationPassed {
		t.Fatalf("scaffolding two same-named files gave duplicate ids: %+v %v", result, err)
	}
}

func TestMetadataValidate_WarnsOnLineCommentMeta(t *testing.T) {
	resetMetadataFlags()
	dir := createTestProject(t, map[string]string{
		"deploy.sql": "SELECT 1;",
		"core/a.sql": "-- <pgmi-meta id=\"11111111-1111-4111-8111-111111111111\" idempotent=\"true\">\n-- </pgmi-meta>\nSELECT 1;\n",
		"core/b.sql": "SELECT 2;",
	})
	result, err := validateProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "./core/a.sql") {
		t.Errorf("warnings = %v, want one naming ./core/a.sql", result.Warnings)
	}
}

// The basic template's run-once files carry id and idempotent but no
// sortKeys, so plan order is path order. A new file there must get no key:
// "./physical/..." sorts before "./semantic/...", any keyed prefix does not.
func TestScaffoldSortKeys_DirectoryWithoutSortKeysGetsNone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sc")
	if err := scaffold.NewScaffolder(false).CreateProject("sc", "basic", dir); err != nil {
		t.Fatal(err)
	}
	newFile := filepath.Join(dir, "physical", "003_due.sql")
	if err := os.WriteFile(newFile, []byte("ALTER TABLE task ADD COLUMN due_on date;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resetMetadataFlags()
	scaffoldWrite, scaffoldIdempotent, scaffoldSortKey = true, false, ""
	if err := runMetadataScaffold(metadataScaffoldCmd, []string{filepath.Join(dir, "physical")}); err != nil {
		t.Fatalf("scaffold refused a directory whose metadata has no sortKeys: %v", err)
	}
	content, _ := os.ReadFile(newFile)
	if strings.Contains(string(content), "<sortKeys>") {
		t.Errorf("block has sortKeys although its neighbours have none:\n%s", content)
	}
	m, err := metadata.Extract(string(content), "physical/003_due.sql")
	if err != nil || m == nil || m.Idempotent == nil || *m.Idempotent {
		t.Fatalf("written block: %+v, %v", m, err)
	}

	result, err := planProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	due, firstSemantic := -1, -1
	for i, e := range result.Plan {
		if e.Path == "./physical/003_due.sql" {
			due = i
		}
		if strings.HasPrefix(e.Path, "./semantic/") && firstSemantic < 0 {
			firstSemantic = i
		}
	}
	if due < 0 || firstSemantic < 0 || due > firstSemantic {
		t.Errorf("./physical/003_due.sql at %d, first ./semantic/ file at %d: the column must come first", due, firstSemantic)
	}
}

func TestMetadataScaffold_WritesAFileThatStillDeploys(t *testing.T) {
	files := map[string]string{
		"deploy.sql": "SELECT 1;",
		"m/a&b.sql":  "SELECT 1;",
		"m/bom.sql":  "\ufeffCREATE TABLE bom(id int);",
	}
	if runtime.GOOS != "windows" {
		files["m/a<b.sql"] = "SELECT 2;"
	}
	dir := createTestProject(t, files)
	if err := scaffoldMeta(t, filepath.Join(dir, "m"), "005/x/"); err != nil {
		t.Fatal(err)
	}
	if _, err := planProject(dir); err != nil {
		t.Fatalf("a scaffolded file no longer parses: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "m", "bom.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "\ufeff/*") || strings.Count(string(content), "\ufeff") != 1 {
		t.Errorf("the BOM must stay first, once: %q", content[:min(len(content), 12)])
	}
}

func TestMetadataScaffold_HintNamesTheFilesDirectory(t *testing.T) {
	dir := createTestProject(t, map[string]string{
		"deploy.sql":         "SELECT 1;",
		"semantic/views.sql": "SELECT 1;",
	})
	err := scaffoldMeta(t, dir, "")
	if err == nil || !strings.Contains(err.Error(), "--sort-key ./semantic/") || strings.Contains(err.Error(), "./physical/") {
		t.Fatalf("the hint must name the file's own directory, got %v", err)
	}
}

func TestMetadataScaffold_SubdirectoryScansFromTheProjectRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sc")
	if err := scaffold.NewScaffolder(false).CreateProject("sc", "basic", dir); err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(dir, "semantic", "active_task.sql")
	backup := filepath.Join(dir, "semantic", "task copy.sql")
	for p, c := range map[string]string{
		view:                              "CREATE OR REPLACE VIEW active_task_view AS SELECT * FROM task WHERE status = 'open';\n",
		backup:                            "SELECT 1;\n",
		filepath.Join(dir, ".pgmiignore"): "semantic/task copy.sql\n",
	} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	err := scaffoldMeta(t, filepath.Join(dir, "semantic"), "")
	if err == nil || !strings.Contains(err.Error(), "--sort-key ./semantic/") {
		t.Fatalf("the hint must name the directory from the project root, got %v", err)
	}
	if err := scaffoldMeta(t, filepath.Join(dir, "semantic"), "./semantic/"); err != nil {
		t.Fatal(err)
	}
	if c, _ := os.ReadFile(backup); string(c) != "SELECT 1;\n" {
		t.Errorf(".pgmiignore at the project root was not honoured:\n%s", c)
	}

	result, err := planProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	active, lastPhysical := -1, -1
	for i, e := range result.Plan {
		switch {
		case e.Path == "./semantic/active_task.sql":
			active = i
		case strings.HasPrefix(e.Path, "./physical/"):
			lastPhysical = i
		}
	}
	if active < 0 || active < lastPhysical {
		t.Errorf("./semantic/active_task.sql at %d, last ./physical/ file at %d: path order must hold", active, lastPhysical)
	}
}
