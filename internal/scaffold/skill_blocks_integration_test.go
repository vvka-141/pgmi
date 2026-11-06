package scaffold_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// A skill block an agent copies verbatim must deploy. A fenced block whose info
// string is "sql runnable target=<path>" is written to <path> in a scaffolded
// project and deployed with the project's tests. kind=tail appends it to
// deploy.sql after the first COMMIT instead (CREATE INDEX CONCURRENTLY and
// friends cannot run inside a plan file). requires=<extension> skips it, with
// the reason logged, when the server lacks that extension. Plain "sql" blocks
// are illustration and never run.
var runnableFence = regexp.MustCompile("(?ms)^```sql runnable([^\n]*)\n(.*?)^```\\s*$")

type skillBlock struct {
	source   string
	target   string
	tail     bool
	requires []string
	sql      string
}

func parseRunnableBlocks(source, markdown string) ([]skillBlock, error) {
	var blocks []skillBlock
	for _, m := range runnableFence.FindAllStringSubmatch(strings.ReplaceAll(markdown, "\r\n", "\n"), -1) {
		b := skillBlock{source: source, sql: m[2]}
		for _, field := range strings.Fields(m[1]) {
			key, value, ok := strings.Cut(field, "=")
			switch {
			case ok && key == "target":
				b.target = filepath.ToSlash(filepath.Clean(value))
			case ok && key == "kind" && value == "tail":
				b.tail = true
			case ok && key == "requires":
				b.requires = append(b.requires, strings.Split(value, ",")...)
			default:
				return nil, fmt.Errorf("%s: unknown runnable attribute %q", source, field)
			}
		}
		if b.tail == (b.target != "") {
			return nil, fmt.Errorf("%s: a runnable block needs exactly one of target=<path> or kind=tail", source)
		}
		if b.target != "" && (strings.HasPrefix(b.target, "..") || filepath.IsAbs(b.target)) {
			return nil, fmt.Errorf("%s: target %q must stay inside the project", source, b.target)
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

func TestParseRunnableBlocks(t *testing.T) {
	md := "intro\n```sql\nSELECT 'illustration';\n```\n" +
		"```sql runnable target=__test__/test_a.sql\nSELECT 1;\n```\n" +
		"```sql runnable kind=tail requires=btree_gist,hstore\nVACUUM;\n```\n"
	blocks, err := parseRunnableBlocks("skill.md", md)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 runnable blocks (plain sql is illustration), got %d", len(blocks))
	}
	if blocks[0].target != "__test__/test_a.sql" || blocks[0].sql != "SELECT 1;\n" {
		t.Errorf("first block parsed as %+v", blocks[0])
	}
	if !blocks[1].tail || !slices.Equal(blocks[1].requires, []string{"btree_gist", "hstore"}) {
		t.Errorf("second block parsed as %+v", blocks[1])
	}

	for name, bad := range map[string]string{
		"no target":       "```sql runnable\nSELECT 1;\n```\n",
		"target and tail": "```sql runnable target=a.sql kind=tail\nSELECT 1;\n```\n",
		"escapes project": "```sql runnable target=../a.sql\nSELECT 1;\n```\n",
		"unknown key":     "```sql runnable target=a.sql flavour=x\nSELECT 1;\n```\n",
	} {
		if _, err := parseRunnableBlocks("skill.md", bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func projectSkillBlocks(t *testing.T, projectDir string) []skillBlock {
	t.Helper()
	var blocks []skillBlock
	root := filepath.Join(projectDir, ".agents", "skills")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(projectDir, p)
		found, err := parseRunnableBlocks(filepath.ToSlash(rel), string(content))
		blocks = append(blocks, found...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return blocks
}

func availableExtensions(t *testing.T, connString string) map[string]bool {
	t.Helper()
	pool := testhelpers.GetTestPool(t, connString, "postgres")
	rows, err := pool.Query(context.Background(), "SELECT name FROM pg_available_extensions")
	if err != nil {
		t.Fatalf("list available extensions: %v", err)
	}
	defer rows.Close()
	available := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		available[name] = true
	}
	return available
}

// deploySkillBlocks writes every runnable block of the project's skills into
// the project, deploys it, and returns how many blocks provably ran. Each block
// gets a NOTICE marker appended, so a block whose file no part of deploy.sql
// executes counts as a failure, not a pass.
func deploySkillBlocks(t *testing.T, connString, template, projectDir, db string) (ran int, err error) {
	t.Helper()
	blocks := projectSkillBlocks(t, projectDir)
	available := availableExtensions(t, connString)

	var markers []string
	for i, b := range blocks {
		if missing := slices.DeleteFunc(slices.Clone(b.requires), func(e string) bool { return available[e] }); len(missing) > 0 {
			t.Logf("SKIPPED %s block %d: the server lacks extension(s) %s", b.source, i+1, strings.Join(missing, ", "))
			continue
		}
		marker := fmt.Sprintf("pgmi-skill-block %d %s", i+1, b.source)
		markers = append(markers, marker)
		body := b.sql + fmt.Sprintf("\nDO $marker$ BEGIN RAISE NOTICE %s; END $marker$;\n", quoteLiteral(marker))

		if b.tail {
			f, err := os.OpenFile(filepath.Join(projectDir, "deploy.sql"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return 0, err
			}
			_, err = f.WriteString("\n" + body)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return 0, err
			}
			continue
		}
		target := filepath.Join(projectDir, filepath.FromSlash(b.target))
		if _, err := os.Stat(target); err == nil {
			return 0, fmt.Errorf("%s block %d: target %s already exists in the %s template", b.source, i+1, b.target, template)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
			return 0, err
		}
	}

	notice, notices := captureNotices()
	params := map[string]string{"env": "dev"}
	if err := testhelpers.NewTestDeployerWithNotices(t, filesystem.NewOSFileSystem(), notice).Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        db,
		SourcePath:          projectDir,
		Overwrite:           true,
		Force:               true,
		Parameters:          params,
	}); err != nil {
		return 0, err
	}

	seen := notices()
	for _, marker := range markers {
		if !slices.Contains(seen, marker) {
			return ran, fmt.Errorf("block %q was written but nothing executed it: put its target where deploy.sql runs files, or in a __test__/ directory", marker)
		}
		ran++
	}
	return ran, nil
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func TestSkillBlocksDeploy(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	for _, template := range []string{"basic", "advanced"} {
		t.Run(template, func(t *testing.T) {
			projectDir := filepath.Join(t.TempDir(), template)
			if err := scaffold.NewScaffolder(false).CreateProject("blocks", template, projectDir); err != nil {
				t.Fatal(err)
			}
			db := "pgmi_skill_blocks_" + template
			defer testhelpers.CleanupTestDB(t, connString, db)

			ran, err := deploySkillBlocks(t, connString, template, projectDir, db)
			if err != nil {
				t.Fatalf("skill blocks of the %s template do not deploy: %v", template, err)
			}
			if ran == 0 {
				t.Fatalf("no runnable skill block ran for the %s template: the gate checks nothing", template)
			}
			t.Logf("%s: %d runnable skill block(s) deployed", template, ran)
		})
	}

	// Agents copy these blocks into projects that switch the guards on, so
	// the blocks must pass them too.
	t.Run("advanced with every guard on", func(t *testing.T) {
		projectDir := filepath.Join(t.TempDir(), "guarded")
		if err := scaffold.NewScaffolder(false).CreateProject("blocks", "advanced", projectDir); err != nil {
			t.Fatal(err)
		}
		enableGuardOptions(t, projectDir, guardOptions...)
		const db = "pgmi_skill_blocks_guarded"
		defer testhelpers.CleanupTestDB(t, connString, db)
		if _, err := deploySkillBlocks(t, connString, "advanced", projectDir, db); err != nil {
			t.Fatalf("skill blocks of the advanced template do not pass its guards: %v", err)
		}
	})
}

// The gate is only worth something if a broken block fails it.
func TestSkillBlocksDeploy_FailsOnBrokenBlock(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectDir := filepath.Join(t.TempDir(), "broken")
	if err := scaffold.NewScaffolder(false).CreateProject("blocks", "basic", projectDir); err != nil {
		t.Fatal(err)
	}
	broken := "---\nname: broken-probe\ndescription: probe\n---\n" +
		"```sql runnable target=__test__/test_broken_probe.sql\nSELECT * FROM no_such_table;\n```\n"
	dir := filepath.Join(projectDir, ".agents", "skills", "broken-probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	db := "pgmi_skill_blocks_broken"
	defer testhelpers.CleanupTestDB(t, connString, db)

	if _, err := deploySkillBlocks(t, connString, "basic", projectDir, db); err == nil || !strings.Contains(err.Error(), "no_such_table") {
		t.Fatalf("a broken block must fail the gate with its own error, got %v", err)
	}
}

// A block whose file nothing runs must fail too, not pass silently.
func TestSkillBlocksDeploy_FailsOnUnexecutedBlock(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectDir := filepath.Join(t.TempDir(), "orphan")
	if err := scaffold.NewScaffolder(false).CreateProject("blocks", "basic", projectDir); err != nil {
		t.Fatal(err)
	}
	orphan := "---\nname: orphan-probe\ndescription: probe\n---\n" +
		"```sql runnable target=elsewhere/never_run.txt\nSELECT 1;\n```\n"
	dir := filepath.Join(projectDir, ".agents", "skills", "orphan-probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(orphan), 0o644); err != nil {
		t.Fatal(err)
	}
	db := "pgmi_skill_blocks_orphan"
	defer testhelpers.CleanupTestDB(t, connString, db)

	if _, err := deploySkillBlocks(t, connString, "basic", projectDir, db); err == nil || !strings.Contains(err.Error(), "nothing executed it") {
		t.Fatalf("an unexecuted block must fail the gate, got %v", err)
	}
}
