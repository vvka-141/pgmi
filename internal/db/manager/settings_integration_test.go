package manager_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// The unit tests assert the deployer HANDS the settings to Create. This asserts
// Create can actually apply them: OWNER and CONNECTION LIMIT ride along on
// CREATE DATABASE, while database GUCs and the comment need their own
// statements afterwards, and each of those can fail on its own.
//
// Measured against PG 17.10 before the fix — overwriting a database configured
// this way returned owner=postgres, connlimit=-1, no GUC and no comment.
func TestManager_CreatePreservesDatabaseProperties(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	const dbName = "pgmi_itest_dbprops"
	const ownerRole = "pgmi_itest_dbprops_owner"

	admin := testhelpers.GetTestPool(t, connString, "postgres")
	defer admin.Close()

	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", ownerRole))
	exec(fmt.Sprintf("CREATE ROLE %s LOGIN", ownerRole))
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
		_, _ = admin.Exec(ctx, fmt.Sprintf("DROP ROLE IF EXISTS %s", ownerRole))
	})

	comment := "kept across the recreate"
	want := &pgmi.DatabaseSettings{
		Owner:           ownerRole,
		ConnectionLimit: 42,
		Options:         []string{"statement_timeout=7s"},
		Comment:         &comment,
	}

	mgr := manager.New()
	pool := testhelpers.GetTestPool(t, connString, "postgres")
	defer pool.Close()
	conn := db.NewPoolAdapter(pool)

	if err := mgr.Create(ctx, conn, dbName, want); err != nil {
		t.Fatalf("create with settings: %v", err)
	}

	got, err := mgr.Settings(ctx, conn, dbName)
	if err != nil {
		t.Fatalf("read back settings: %v", err)
	}
	if got == nil {
		t.Fatal("no settings reported for a database that exists")
	}
	if got.Owner != want.Owner {
		t.Errorf("owner = %q, want %q", got.Owner, want.Owner)
	}
	if got.ConnectionLimit != want.ConnectionLimit {
		t.Errorf("connection limit = %d, want %d", got.ConnectionLimit, want.ConnectionLimit)
	}
	if len(got.Options) != 1 || got.Options[0] != "statement_timeout=7s" {
		t.Errorf("database settings = %v, want [statement_timeout=7s]", got.Options)
	}
	if got.Comment == nil || *got.Comment != comment {
		t.Errorf("comment = %v, want %q", got.Comment, comment)
	}
}

// A database matching the server default reports nothing to preserve for its
// locale, so recreating it keeps the plain CREATE DATABASE path and goes on
// inheriting template1.
func TestManager_SettingsFlagsOnlyARealLocaleDifference(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	const dbName = "pgmi_itest_dblocale"
	pool := testhelpers.GetTestPool(t, connString, "postgres")
	defer pool.Close()
	conn := db.NewPoolAdapter(pool)

	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName)); err != nil {
		t.Fatalf("drop: %v", err)
	}
	mgr := manager.New()
	if err := mgr.Create(ctx, conn, dbName, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	})

	got, err := mgr.Settings(ctx, conn, dbName)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if got == nil {
		t.Fatal("no settings reported for a database that exists")
	}
	if got.PreserveLocale {
		t.Errorf("a default database was flagged as needing template0: %+v", *got)
	}

	// And an absent database has nothing to report at all.
	absent, err := mgr.Settings(ctx, conn, "pgmi_itest_definitely_absent")
	if err != nil {
		t.Fatalf("settings for an absent database: %v", err)
	}
	if absent != nil {
		t.Errorf("settings reported for an absent database: %+v", *absent)
	}
}
