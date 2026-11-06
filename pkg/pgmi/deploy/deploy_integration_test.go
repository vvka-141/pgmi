package deploy_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
	"github.com/vvka-141/pgmi/pkg/pgmi/deploy"
)

func TestLibraryDeploy(t *testing.T) {
	testhelpers.SkipIfShort(t)
	connString := testhelpers.GetTestConnectionString(t)
	const dbName = "pgmi_library_deploy"
	testhelpers.CleanupTestDB(t, connString, dbName)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, dbName) })

	dir := t.TempDir()
	files := map[string]string{
		"deploy.sql": "BEGIN;\nDO $$ BEGIN EXECUTE (SELECT content FROM pg_temp.pgmi_source_view WHERE path = './schema.sql'); END $$;\nCOMMIT;\nSELECT 1;\n",
		"schema.sql": "CREATE TABLE greeting (word text);\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := pgmi.DeploymentConfig{
		SourcePath:          dir,
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        dbName,
	}
	result, err := deploy.New().Deploy(context.Background(), cfg)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if result.Database != dbName || !result.Created || result.ExecutionUnits != 2 || result.UnitsCommitted != 2 || result.FilesLoaded == 0 {
		t.Fatalf("result = %+v", result)
	}

	pool := testhelpers.GetTestPool(t, connString, dbName)
	var exists bool
	if err := pool.QueryRow(context.Background(), "SELECT to_regclass('public.greeting') IS NOT NULL").Scan(&exists); err != nil || !exists {
		t.Fatalf("the deploy did not create its table: exists=%v err=%v", exists, err)
	}
	pool.Close()

	cfg.Overwrite = true
	result, err = deploy.New().Deploy(context.Background(), cfg)
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitApprovalDenied {
		t.Fatalf("an overwrite without Force: exit %d, want %d: %v", got, pgmi.ExitApprovalDenied, err)
	}
	if result == nil || result.Database != dbName {
		t.Fatalf("a refused deploy still answers its result, got %+v", result)
	}
}

func TestLibraryDeploy_ConcurrentDeploysKeepTheirOwnNotices(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const notices = 20
	dbs := []string{"pgmi_library_notices_a", "pgmi_library_notices_b"}

	got := make([][]string, len(dbs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, len(dbs))
	for i, dbName := range dbs {
		testhelpers.CleanupTestDB(t, connString, dbName)
		t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, dbName) })

		dir := t.TempDir()
		deploySQL := fmt.Sprintf("DO $$ BEGIN FOR i IN 1..%d LOOP RAISE NOTICE 'from %s %%', i; PERFORM pg_sleep(0.02); END LOOP; END $$;\n", notices, dbName)
		if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte(deploySQL), 0o644); err != nil {
			t.Fatal(err)
		}
		d := deploy.New(deploy.WithNoticeHandler(func(_, message, _, _ string) {
			mu.Lock()
			got[i] = append(got[i], message)
			mu.Unlock()
		}))
		wg.Go(func() {
			<-start
			_, errs[i] = d.Deploy(context.Background(), pgmi.DeploymentConfig{
				SourcePath:          dir,
				ConnectionString:    connString,
				MaintenanceDatabase: "postgres",
				DatabaseName:        dbName,
			})
		})
	}
	close(start)
	wg.Wait()

	for i, dbName := range dbs {
		if errs[i] != nil {
			t.Fatalf("deploy %s: %v", dbName, errs[i])
		}
		if len(got[i]) != notices {
			t.Errorf("handler of %s got %d notices, want %d: %q", dbName, len(got[i]), notices, got[i])
		}
		for _, m := range got[i] {
			if !strings.HasPrefix(m, "from "+dbName+" ") {
				t.Errorf("handler of %s got another deploy's notice %q", dbName, m)
				break
			}
		}
	}
}

func TestLibraryDeploy_TheContextDeadlineBoundsTheDeploy(t *testing.T) {
	testhelpers.SkipIfShort(t)
	connString := testhelpers.GetTestConnectionString(t)
	const dbName = "pgmi_library_deadline"
	testhelpers.CleanupTestDB(t, connString, dbName)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, dbName) })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte("SELECT pg_sleep(30);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := deploy.New().Deploy(ctx, pgmi.DeploymentConfig{
		SourcePath: dir, ConnectionString: connString, MaintenanceDatabase: "postgres", DatabaseName: dbName,
	})
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitTimeout || time.Since(start) > 15*time.Second {
		t.Fatalf("exit %d after %s, want %d within the deadline: %v", got, time.Since(start).Round(time.Millisecond), pgmi.ExitTimeout, err)
	}
}
