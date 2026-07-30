package services_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/services"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/internal/testing/fixtures"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// countReleases swaps the release seam for one that counts calls and still
// performs the real unlock.
func countReleases(t *testing.T, n *int) {
	t.Helper()

	original := services.ReleaseDeployLock
	services.ReleaseDeployLock = func(ctx context.Context, conn *pgxpool.Conn) {
		*n++
		original(ctx, conn)
	}
	t.Cleanup(func() { services.ReleaseDeployLock = original })
}

func lockTestManager(t *testing.T, connString, testDB string) (*services.SessionManager, *pgmi.ConnectionConfig, pgmi.FileScanResult) {
	t.Helper()

	connConfig, err := db.ParseConnectionString(connString)
	if err != nil {
		t.Fatalf("Failed to parse connection string: %v", err)
	}
	connConfig.Database = testDB

	fileScanner := scanner.NewScannerWithFS(checksum.New(), fixtures.StandardMultiLevel())
	sm := services.NewSessionManager(db.NewConnector, fileScanner, loader.NewLoader(), logging.NewNullLogger())
	return sm, connConfig, mustScanProject(t, sm)
}

// PrepareSession must release the deploy advisory lock on every path that
// abandons a half-prepared session rather than leaving it to the disconnect.
// An invalid --compat fails inside prepareSessionTables, which runs after the
// lock is taken.
//
// The assertion counts releases instead of observing the lock from a second
// session: the end-to-end symptom is a spurious ErrConcurrentDeploy on an
// immediate retry, and that depends on how fast the server reaps a
// disconnected backend — over loopback it is reaped so quickly that a retry
// succeeds even when nothing released the lock, which makes the obvious test
// vacuous (verified: 40 runs, zero failures, with the release disabled).
func TestPrepareSession_ReleasesLockOnFailureAfterAcquire(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	cleanup := testhelpers.CreateTestDB(t, connString, "pgmi_test_session_lock_release")
	defer cleanup()

	sm, connConfig, scanResult := lockTestManager(t, connString, "pgmi_test_session_lock_release")

	var released int
	countReleases(t, &released)

	if _, err := sm.PrepareSession(ctx, connConfig, scanResult, nil, "99", false); err == nil {
		t.Fatal("Expected an unsupported --compat to fail session preparation")
	}
	if released != 1 {
		t.Fatalf("Failure after acquiring the lock released it %d times, want 1", released)
	}

	// The retry is the symptom this protects: it must not be refused as a
	// concurrent deploy.
	session, err := sm.PrepareSession(ctx, connConfig, scanResult, nil, "", false)
	if err != nil {
		t.Fatalf("Retry after a failed preparation should succeed: %v", err)
	}
	// Still 1: a successful preparation hands the lock to the Session, and
	// Session.Close releases it through pkg/pgmi, not through this seam.
	// TestReleaseDeployLock_* in pkg/pgmi covers that half.
	if released != 1 {
		t.Errorf("A successful preparation released the lock; want the handover to Close, got %d releases", released)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// A refused preparation holds no lock, so it must not run pg_advisory_unlock_all
// — that is per-session, and calling it here would be a no-op today but would
// mask a real mistake if the lock were ever taken before the contention check.
func TestPrepareSession_DoesNotReleaseLockItNeverAcquired(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	cleanup := testhelpers.CreateTestDB(t, connString, "pgmi_test_session_lock_contended")
	defer cleanup()

	sm, connConfig, scanResult := lockTestManager(t, connString, "pgmi_test_session_lock_contended")

	holder, err := sm.PrepareSession(ctx, connConfig, scanResult, nil, "", false)
	if err != nil {
		t.Fatalf("First PrepareSession failed: %v", err)
	}
	defer holder.Close()

	var released int
	countReleases(t, &released)

	_, err = sm.PrepareSession(ctx, connConfig, scanResult, nil, "", false)
	if err == nil {
		t.Fatal("Expected a second concurrent preparation to be refused")
	}
	if pgmi.ExitCodeForError(err) != pgmi.ExitConcurrentDeploy {
		t.Fatalf("Expected ErrConcurrentDeploy, got: %v", err)
	}
	if released != 0 {
		t.Errorf("Refused preparation called the unlock %d times; it holds no lock", released)
	}
}
