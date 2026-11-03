package manager_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/testhelpers"
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

// --overwrite drops and recreates the database. A database that had PUBLIC's
// default CONNECT/TEMPORARY revoked came back open to every role (PGMI-382).
func TestManager_CreatePreservesDatabaseACL(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	const dbName = "pgmi_itest_dbacl"
	const reader = "pgmi_itest_dbacl_reader"

	admin := testhelpers.GetTestPool(t, connString, "postgres")
	defer admin.Close()
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	datacl := func() string {
		t.Helper()
		var acl *string
		if err := admin.QueryRow(ctx, "SELECT datacl::text FROM pg_database WHERE datname = $1", dbName).Scan(&acl); err != nil {
			t.Fatalf("read datacl: %v", err)
		}
		if acl == nil {
			return "<default>"
		}
		return *acl
	}

	exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", reader))
	exec(fmt.Sprintf("CREATE ROLE %s", reader))
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
		_, _ = admin.Exec(ctx, fmt.Sprintf("DROP ROLE IF EXISTS %s", reader))
	})
	exec(fmt.Sprintf("CREATE DATABASE %s", dbName))
	exec(fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM PUBLIC", dbName))
	exec(fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s WITH GRANT OPTION", dbName, reader))
	before := datacl()

	mgr := manager.New()
	conn := db.NewPoolAdapter(admin)
	settings, err := mgr.Settings(ctx, conn, dbName)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	exec(fmt.Sprintf("DROP DATABASE %s", dbName))
	if err := mgr.Create(ctx, conn, dbName, settings); err != nil {
		t.Fatalf("create with settings: %v", err)
	}

	if after := datacl(); after != before {
		t.Errorf("datacl after recreate = %s, want %s", after, before)
	}
}

// --overwrite read a list setting back as one quoted string, so search_path
// lost public; it read only libc locales, so an ICU database came back libc;
// and it read only database-wide settings, so ALTER ROLE ... IN DATABASE was
// lost. Each is compared catalog to catalog across a recreate.
func TestManager_RecreateKeepsListSettingsLocaleProviderAndRoleSettings(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	const dbName = "pgmi_itest_dbrecreate"
	const role = "pgmi_itest_dbrecreate_role"

	admin := testhelpers.GetTestPool(t, connString, "postgres")
	defer admin.Close()
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var icu bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_collation WHERE collprovider = 'i')").Scan(&icu); err != nil {
		t.Fatal(err)
	}
	exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", role))
	exec(fmt.Sprintf("CREATE ROLE %s", role))
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
		_, _ = admin.Exec(ctx, fmt.Sprintf("DROP ROLE IF EXISTS %s", role))
	})
	if icu {
		exec(fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0 LOCALE_PROVIDER icu ICU_LOCALE 'de-DE' LOCALE 'C'", dbName))
	} else {
		exec(fmt.Sprintf("CREATE DATABASE %s", dbName))
	}
	exec(fmt.Sprintf(`ALTER DATABASE %s SET search_path = "$user", public, "Odd Schema"`, dbName))
	exec(fmt.Sprintf(`ALTER DATABASE %s SET statement_timeout = '7s'`, dbName))
	exec(fmt.Sprintf(`ALTER DATABASE %s SET application_name = 'a, b'`, dbName))
	exec(fmt.Sprintf(`ALTER ROLE %s IN DATABASE %s SET work_mem = '77MB'`, role, dbName))

	snapshot := func() string {
		t.Helper()
		var s string
		err := admin.QueryRow(ctx, `
			SELECT d.datlocprovider::text || '|' || coalesce(to_jsonb(d)->>'datlocale', to_jsonb(d)->>'daticulocale', '') || '|' ||
			       coalesce((SELECT string_agg(coalesce(pg_get_userbyid(nullif(s.setrole, 0)), '') || ':' || array_to_string(s.setconfig, ';'), ' / '
			                                   ORDER BY s.setrole)
			                 FROM pg_db_role_setting s WHERE s.setdatabase = d.oid), '')
			FROM pg_database d WHERE d.datname = $1`, dbName).Scan(&s)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return s
	}
	before := snapshot()

	mgr := manager.New()
	pool := testhelpers.GetTestPool(t, connString, "postgres")
	defer pool.Close()
	conn := db.NewPoolAdapter(pool)

	settings, err := mgr.Settings(ctx, conn, dbName)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if err := mgr.Drop(ctx, conn, dbName); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := mgr.Create(ctx, conn, dbName, settings); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if after := snapshot(); after != before {
		t.Errorf("recreate changed the database:\n before %s\n after  %s", before, after)
	}
}

// The deploy session holds hashtextextended('pgmi.deploy.' || db, 0). --overwrite
// reads it from pg_locks before ending connections; a key with the sign bit set
// is where reassembling classid and objid can go wrong, so both signs are held.
func TestManager_DeployInProgressSeesTheSessionLock(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	admin := testhelpers.GetTestPool(t, connString, "postgres")
	defer admin.Close()
	conn := db.NewPoolAdapter(admin)
	mgr := manager.New()

	var names []string
	for _, sign := range []string{"> 0", "< 0"} {
		var name string
		err := admin.QueryRow(ctx, `
			SELECT n FROM generate_series(0, 99) i, LATERAL (SELECT 'pgmi_itest_lock_' || i AS n) x
			WHERE hashtextextended('pgmi.deploy.' || n, 0) `+sign+` ORDER BY i LIMIT 1`).Scan(&name)
		if err != nil {
			t.Fatalf("pick a name whose key is %s: %v", sign, err)
		}
		names = append(names, name)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if _, err := admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", name)); err != nil {
				t.Fatal(err)
			}
			if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", name)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name)) })

			holder := testhelpers.GetTestPool(t, connString, name)
			defer holder.Close()
			lockConn, err := holder.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lockConn.Release()

			if running, err := mgr.DeployInProgress(ctx, conn, name); err != nil || running {
				t.Fatalf("before the lock: running=%v err=%v", running, err)
			}
			if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('pgmi.deploy.' || current_database(), 0))`); err != nil {
				t.Fatal(err)
			}
			if running, err := mgr.DeployInProgress(ctx, conn, name); err != nil || !running {
				t.Fatalf("while a deploy holds the lock: running=%v err=%v", running, err)
			}
			if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_unlock_all()`); err != nil {
				t.Fatal(err)
			}
			if running, err := mgr.DeployInProgress(ctx, conn, name); err != nil || running {
				t.Fatalf("after the lock: running=%v err=%v", running, err)
			}
		})
	}
}
