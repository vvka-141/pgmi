package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/metadata"
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
