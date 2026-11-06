package services

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/session"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type mockConnector struct {
	pool *pgxpool.Pool
	err  error
}

func (m *mockConnector) Connect(_ context.Context) (*pgxpool.Pool, error) {
	return m.pool, m.err
}

type mockApprover struct {
	approved bool
	err      error
}

func (m *mockApprover) RequestApproval(_ context.Context, _ string) (bool, error) {
	return m.approved, m.err
}

type mockSessionPreparer struct {
	session *session.Session
	err     error
	scanErr error
}

func (m *mockSessionPreparer) ScanProject(_ string) (scanner.Result, error) {
	return scanner.Result{}, m.scanErr
}

func (m *mockSessionPreparer) PrepareSession(_ context.Context, _ *db.ConnectionConfig, _ scanner.Result, _ map[string]string, _ string, _ bool) (*session.Session, error) {
	return m.session, m.err
}

type mockDBConnection struct {
	execErr    error
	queryRow   db.Row
	acquireErr error
}

func (m *mockDBConnection) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, m.execErr
}

func (m *mockDBConnection) QueryRow(_ context.Context, _ string, _ ...any) db.Row {
	return m.queryRow
}

func (m *mockDBConnection) Acquire(_ context.Context) (db.PooledConnection, error) {
	return nil, m.acquireErr
}

type mockFileScanner struct {
	scanResult  scanner.Result
	scanErr     error
	validateErr error
	readContent string
	readErr     error
}

func (m *mockFileScanner) ScanDirectory(_ string) (scanner.Result, error) {
	return m.scanResult, m.scanErr
}

func (m *mockFileScanner) ValidateDeploySQL(_ string) error {
	return m.validateErr
}

func (m *mockFileScanner) ReadDeploySQL(_ string) (string, error) {
	return m.readContent, m.readErr
}

type mockFileLoader struct {
	loadFilesErr  error
	loadParamsErr error
}

func (m *mockFileLoader) LoadFilesIntoSession(_ context.Context, _ *pgxpool.Conn, _ []pgmi.FileMetadata) error {
	return m.loadFilesErr
}

func (m *mockFileLoader) LoadParametersIntoSession(_ context.Context, _ *pgxpool.Conn, _ map[string]string) error {
	return m.loadParamsErr
}

type mockDatabaseManager struct {
	existsResult bool
	existsErr    error
	createErr    error
	dropErr      error

	deployInProgress bool
	overwriteBusy    bool
	unlocked         int

	// Recorded so a test can assert the destructive call did NOT happen.
	// Returning the right error is not the same as not dropping the database.
	dropped []string

	// What Settings reports, and what Create was actually handed — an
	// --overwrite must recreate the database the way it found it.
	settings    *manager.DatabaseSettings
	settingsErr error
	restoreGaps []string
	createdWith *manager.DatabaseSettings
	created     int

	// What the server reports; zero means a supported one.
	serverVersionNum int
}

func (m *mockDatabaseManager) Exists(_ context.Context, _ db.DBConnection, _ string) (bool, error) {
	return m.existsResult, m.existsErr
}

func (m *mockDatabaseManager) Settings(_ context.Context, _ db.DBConnection, _ string) (*manager.DatabaseSettings, error) {
	return m.settings, m.settingsErr
}

func (m *mockDatabaseManager) Create(_ context.Context, _ db.DBConnection, _ string, settings *manager.DatabaseSettings) error {
	m.createdWith = settings
	m.created++
	return m.createErr
}

func (m *mockDatabaseManager) Drop(_ context.Context, _ db.DBConnection, name string) error {
	m.dropped = append(m.dropped, name)
	return m.dropErr
}

func (m *mockDatabaseManager) LockOverwrite(_ context.Context, conn db.DBConnection, _ string) (db.DBConnection, func(), error) {
	if m.overwriteBusy {
		return nil, nil, nil
	}
	return conn, func() { m.unlocked++ }, nil
}

func (m *mockDatabaseManager) ServerVersion(_ context.Context, _ db.DBConnection) (int, string, error) {
	if m.serverVersionNum == 0 {
		return 180000, "18.0", nil
	}
	return m.serverVersionNum, fmt.Sprintf("%d.0", m.serverVersionNum/10000), nil
}

func (m *mockDatabaseManager) RestoreGaps(_ context.Context, _ db.DBConnection, _ string) ([]string, error) {
	return m.restoreGaps, nil
}

func (m *mockDatabaseManager) DeployInProgress(_ context.Context, _ db.DBConnection, _ string) (bool, error) {
	return m.deployInProgress, nil
}

type mockLogger struct{}

func (m *mockLogger) Verbose(_ string, _ ...any) {}
func (m *mockLogger) Info(_ string, _ ...any)    {}
func (m *mockLogger) Error(_ string, _ ...any)   {}
