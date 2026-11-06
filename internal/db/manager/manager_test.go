package manager_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// mockDBConnection is a test double for db.DBConnection
type mockDBConnection struct {
	execFunc     func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	queryRowFunc func(ctx context.Context, sql string, args ...any) db.Row
	acquireFunc  func(ctx context.Context) (db.PooledConnection, error)
}

func (m *mockDBConnection) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if m.execFunc != nil {
		return m.execFunc(ctx, sql, args...)
	}
	return pgconn.CommandTag{}, nil
}

func (m *mockDBConnection) QueryRow(ctx context.Context, sql string, args ...any) db.Row {
	if m.queryRowFunc != nil {
		return m.queryRowFunc(ctx, sql, args...)
	}
	return &mockRow{}
}

func (m *mockDBConnection) Acquire(ctx context.Context) (db.PooledConnection, error) {
	if m.acquireFunc != nil {
		return m.acquireFunc(ctx)
	}
	return &mockPooledConnection{}, nil
}

// mockRow is a test double for db.Row
type mockRow struct {
	scanFunc func(dest ...any) error
}

func (m *mockRow) Scan(dest ...any) error {
	if m.scanFunc != nil {
		return m.scanFunc(dest...)
	}
	return nil
}

// mockPooledConnection is a test double for db.PooledConnection
type mockPooledConnection struct {
	execFunc    func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	releaseFunc func()
}

func (m *mockPooledConnection) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if m.execFunc != nil {
		return m.execFunc(ctx, sql, args...)
	}
	return pgconn.CommandTag{}, nil
}

func (m *mockPooledConnection) QueryRow(_ context.Context, _ string, _ ...any) db.Row {
	return nil
}

func (m *mockPooledConnection) Release() {
	if m.releaseFunc != nil {
		m.releaseFunc()
	}
}

func TestManager_Create_WithSpecialCharsInName(t *testing.T) {
	testCases := []struct {
		name   string
		dbName string
	}{
		{"Database with spaces", "my database"},
		{"Database with quotes", `my"database`},
		{"Database with backticks", "my`database"},
		{"Database with semicolon", "my;database"},
		{"Database with dash", "my-database"},
		{"Database with underscore", "my_database"},
		{"Database with numbers", "database123"},
		{"Mixed special characters", "my-db_2024"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mgr := manager.New()

			// Track what SQL was executed
			var executedSQL string
			mockConn := &mockDBConnection{
				acquireFunc: func(ctx context.Context) (db.PooledConnection, error) {
					return &mockPooledConnection{
						execFunc: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
							executedSQL = sql
							return pgconn.CommandTag{}, nil
						},
					}, nil
				},
			}

			err := mgr.Create(ctx, mockConn, tc.dbName, nil)
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}

			// Verify SQL uses proper quoting (should contain quoted identifier)
			if executedSQL == "" {
				t.Fatal("Expected SQL to be executed")
			}

			// The whole statement, not a prefix: a prefix check passes with no
			// quoting at all.
			want := "CREATE DATABASE " + pgx.Identifier{tc.dbName}.Sanitize()
			if executedSQL != want {
				t.Errorf("executed %q, want %q", executedSQL, want)
			}
		})
	}
}

func TestManager_Create_SQLInjectionAttempt(t *testing.T) {
	testCases := []struct {
		name         string
		dbName       string
		shouldReject bool // pgx.Identifier.Sanitize() handles most cases, so we expect safe quoting
	}{
		{
			name:         "Injection with DROP",
			dbName:       "test; DROP DATABASE postgres; --",
			shouldReject: false, // Will be safely quoted
		},
		{
			name:         "Injection with comment",
			dbName:       "test -- comment",
			shouldReject: false, // Will be safely quoted
		},
		{
			name:         "Injection with newline",
			dbName:       "test\nDROP DATABASE postgres",
			shouldReject: false, // Will be safely quoted
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mgr := manager.New()

			var executedSQL string
			mockConn := &mockDBConnection{
				acquireFunc: func(ctx context.Context) (db.PooledConnection, error) {
					return &mockPooledConnection{
						execFunc: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
							executedSQL = sql
							return pgconn.CommandTag{}, nil
						},
					}, nil
				},
			}

			err := mgr.Create(ctx, mockConn, tc.dbName, nil)
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}

			// Verify the SQL doesn't contain unescaped malicious content
			// pgx.Identifier.Sanitize() properly quotes and escapes
			t.Logf("Malicious input: %s", tc.dbName)
			t.Logf("Sanitized SQL: %s", executedSQL)

			// Exactly one quoted identifier: anything else lets the input end the
			// statement or smuggle a second one.
			want := "CREATE DATABASE " + pgx.Identifier{tc.dbName}.Sanitize()
			if executedSQL != want {
				t.Errorf("executed %q, want %q", executedSQL, want)
			}
		})
	}
}

func TestManager_Drop_NonExistentDatabase(t *testing.T) {
	ctx := context.Background()
	mgr := manager.New()

	// Simulate DROP DATABASE failing because database doesn't exist
	mockConn := &mockDBConnection{
		acquireFunc: func(ctx context.Context) (db.PooledConnection, error) {
			return &mockPooledConnection{
				execFunc: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
					// Simulate PostgreSQL error
					return pgconn.CommandTag{}, errors.New(`database "nonexistent" does not exist`)
				},
			}, nil
		},
	}

	err := mgr.Drop(ctx, mockConn, "nonexistent")
	if err == nil {
		t.Fatal("Expected error when dropping non-existent database")
	}

	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should contain database name, got: %s", err)
	}
}

func TestManager_Exists_DatabaseExists(t *testing.T) {
	ctx := context.Background()
	mgr := manager.New()

	mockConn := &mockDBConnection{
		queryRowFunc: func(ctx context.Context, sql string, args ...any) db.Row {
			return &mockRow{
				scanFunc: func(dest ...any) error {
					// Set the result to true (database exists)
					if len(dest) == 1 {
						if ptr, ok := dest[0].(*bool); ok {
							*ptr = true
						}
					}
					return nil
				},
			}
		},
	}

	exists, err := mgr.Exists(ctx, mockConn, "mydb")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if !exists {
		t.Error("Expected database to exist")
	}
}

func TestManager_Exists_DatabaseDoesNotExist(t *testing.T) {
	ctx := context.Background()
	mgr := manager.New()

	mockConn := &mockDBConnection{
		queryRowFunc: func(ctx context.Context, sql string, args ...any) db.Row {
			return &mockRow{
				scanFunc: func(dest ...any) error {
					// Set the result to false (database doesn't exist)
					if len(dest) == 1 {
						if ptr, ok := dest[0].(*bool); ok {
							*ptr = false
						}
					}
					return nil
				},
			}
		},
	}

	exists, err := mgr.Exists(ctx, mockConn, "nonexistent")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if exists {
		t.Error("Expected database to not exist")
	}
}

func TestManager_Exists_QueryError(t *testing.T) {
	ctx := context.Background()
	mgr := manager.New()

	expectedErr := errors.New("connection lost")
	mockConn := &mockDBConnection{
		queryRowFunc: func(ctx context.Context, sql string, args ...any) db.Row {
			return &mockRow{
				scanFunc: func(dest ...any) error {
					return expectedErr
				},
			}
		},
	}

	_, err := mgr.Exists(ctx, mockConn, "mydb")
	if err == nil {
		t.Fatal("Expected error from query failure")
	}

	// Should wrap the error
	if !errors.Is(err, expectedErr) {
		t.Errorf("Expected wrapped error, got: %v", err)
	}
}

func TestManager_Create_ConnectionAcquireFailure(t *testing.T) {
	ctx := context.Background()
	mgr := manager.New()

	expectedErr := errors.New("pool exhausted")
	mockConn := &mockDBConnection{
		acquireFunc: func(ctx context.Context) (db.PooledConnection, error) {
			return nil, expectedErr
		},
	}

	err := mgr.Create(ctx, mockConn, "mydb", nil)
	if err == nil {
		t.Fatal("Expected error from connection acquire failure")
	}

	if !errors.Is(err, expectedErr) {
		t.Errorf("Expected wrapped error, got: %v", err)
	}
}

func TestManager_Drop_ConnectionAcquireFailure(t *testing.T) {
	ctx := context.Background()
	mgr := manager.New()

	expectedErr := errors.New("pool exhausted")
	mockConn := &mockDBConnection{
		acquireFunc: func(ctx context.Context) (db.PooledConnection, error) {
			return nil, expectedErr
		},
	}

	err := mgr.Drop(ctx, mockConn, "mydb")
	if err == nil {
		t.Fatal("Expected error from connection acquire failure")
	}

	if !errors.Is(err, expectedErr) {
		t.Errorf("Expected wrapped error, got: %v", err)
	}
}

func TestManager_Create_AttemptsEveryRestoreAndReportsAllFailures(t *testing.T) {
	var executed []string
	conn := &mockDBConnection{acquireFunc: func(context.Context) (db.PooledConnection, error) {
		return &mockPooledConnection{execFunc: func(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
			executed = append(executed, sql)
			if strings.Contains(sql, "log_min_duration_statement") || strings.Contains(sql, "lock_timeout") {
				return pgconn.CommandTag{}, &pgconn.PgError{Code: "42501", Message: "permission denied to set parameter"}
			}
			return pgconn.CommandTag{}, nil
		}}, nil
	}}
	comment := "kept"
	settings := &manager.DatabaseSettings{
		ConnectionLimit: -1,
		Options:         []string{"log_min_duration_statement=100", "work_mem=8MB"},
		RoleOptions:     []manager.DatabaseRoleOptions{{Role: "app", Options: []string{"lock_timeout=1s", "search_path=app"}}},
		Comment:         &comment,
	}

	err := manager.New().Create(context.Background(), conn, "db", settings)

	if !errors.Is(err, pgmi.ErrExecutionFailed) {
		t.Fatalf("error %v, want ErrExecutionFailed (exit 13)", err)
	}
	for _, want := range []string{`"log_min_duration_statement"`, `"lock_timeout" of role "app"`, "2 of its settings"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
	for _, want := range []string{"work_mem", "search_path", "COMMENT ON DATABASE"} {
		if !slices.ContainsFunc(executed, func(s string) bool { return strings.Contains(s, want) }) {
			t.Errorf("%s was not attempted after an earlier failure; executed: %q", want, executed)
		}
	}
}
