package scaffold_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Each "Safe to delete" item of the advanced README, by the paths it names.
var advancedTrimmable = [][]string{
	{"gateways/"},
	{"api/dev/"},
	{"lib/core/change-tracking.sql", "lib/core/change-tracking-actor.sql", "lib/core/change-tracking-functions.sql", "lib/__test__/test_change_tracking.sql"},
	{"lib/core/entity-registry.sql", "lib/core/entity-registry-functions.sql", "lib/__test__/test_entity_registry.sql"},
	{"lib/core/organization-counter.sql", "lib/core/organization-counter-functions.sql", "lib/__test__/test_organization_counter.sql"},
}

// The README's list is the promise; this keeps it equal to what is proved.
func TestAdvancedReadmeTrimmingListMatchesTheTest(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("../../shared/templates", "advanced", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	section := string(body)
	start := strings.Index(section, "**Safe to delete**")
	end := strings.Index(section, "**Keep, and change instead:**")
	if start < 0 || end < start {
		t.Fatal("the advanced README has no \"Safe to delete\" list before \"Keep, and change instead\"")
	}
	paths := regexp.MustCompile("`([a-z_./-]+/?)` *(?:,|—)")
	var got [][]string
	for _, line := range strings.Split(section[start:end], "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		var item []string
		for _, m := range paths.FindAllStringSubmatch(line, -1) {
			item = append(item, m[1])
		}
		got = append(got, item)
	}
	if fmt.Sprint(got) != fmt.Sprint(advancedTrimmable) {
		t.Errorf("README \"Safe to delete\" lists\n  %v\nbut the trimming test deletes\n  %v", got, advancedTrimmable)
	}
}

// Deleting a listed item must leave a template that deploys, twice.
func TestAdvancedTemplate_TrimmingList(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	for i, item := range advancedTrimmable {
		t.Run(strings.Join(item, "+"), func(t *testing.T) {
			projectPath := filepath.Join(t.TempDir(), "trim")
			if err := scaffold.NewScaffolder(false).CreateProject("trim", "advanced", projectPath); err != nil {
				t.Fatal(err)
			}
			for _, p := range item {
				full := filepath.Join(projectPath, filepath.FromSlash(p))
				if _, err := os.Stat(full); err != nil {
					t.Fatalf("listed path %s is not in the scaffold: %v", p, err)
				}
				if err := os.RemoveAll(full); err != nil {
					t.Fatal(err)
				}
			}
			db := fmt.Sprintf("pgmi_advanced_trim_%d", i)
			defer testhelpers.CleanupTestDB(t, connString, db)
			for _, overwrite := range []bool{true, false} {
				err := testhelpers.NewTestDeployer(t).Deploy(context.Background(), pgmi.DeploymentConfig{
					ConnectionString:    connString,
					MaintenanceDatabase: "postgres",
					DatabaseName:        db,
					SourcePath:          projectPath,
					Overwrite:           overwrite,
					Force:               overwrite,
					Parameters:          map[string]string{"env": "dev"},
				})
				if err != nil {
					t.Fatalf("deploy (overwrite=%v) without %v: %v", overwrite, item, err)
				}
			}
		})
	}
}
