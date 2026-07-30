package services

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	session *pgmi.Session
	err     error
	scanErr error
}

func (m *mockSessionPreparer) ScanProject(_ string) (pgmi.FileScanResult, error) {
	return pgmi.FileScanResult{}, m.scanErr
}

func (m *mockSessionPreparer) PrepareSession(_ context.Context, _ *pgmi.ConnectionConfig, _ pgmi.FileScanResult, _ map[string]string, _ string, _ bool) (*pgmi.Session, error) {
	return m.session, m.err
}

type mockDBConnection struct {
	execErr    error
	queryRow   pgmi.Row
	acquireErr error
}

func (m *mockDBConnection) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, m.execErr
}

func (m *mockDBConnection) QueryRow(_ context.Context, _ string, _ ...any) pgmi.Row {
	return m.queryRow
}

func (m *mockDBConnection) Acquire(_ context.Context) (pgmi.PooledConnection, error) {
	return nil, m.acquireErr
}

type mockFileScanner struct {
	scanResult  pgmi.FileScanResult
	scanErr     error
	validateErr error
	readContent string
	readErr     error
}

func (m *mockFileScanner) ScanDirectory(_ string) (pgmi.FileScanResult, error) {
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
	terminateErr error

	// Recorded so a test can assert the destructive call did NOT happen.
	// Returning the right error is not the same as not dropping the database.
	dropped []string

	// What Settings reports, and what Create was actually handed — an
	// --overwrite must recreate the database the way it found it.
	settings    *pgmi.DatabaseSettings
	settingsErr error
	createdWith *pgmi.DatabaseSettings
}

func (m *mockDatabaseManager) Exists(_ context.Context, _ pgmi.DBConnection, _ string) (bool, error) {
	return m.existsResult, m.existsErr
}

func (m *mockDatabaseManager) Settings(_ context.Context, _ pgmi.DBConnection, _ string) (*pgmi.DatabaseSettings, error) {
	return m.settings, m.settingsErr
}

func (m *mockDatabaseManager) Create(_ context.Context, _ pgmi.DBConnection, _ string, settings *pgmi.DatabaseSettings) error {
	m.createdWith = settings
	return m.createErr
}

func (m *mockDatabaseManager) Drop(_ context.Context, _ pgmi.DBConnection, name string) error {
	m.dropped = append(m.dropped, name)
	return m.dropErr
}

func (m *mockDatabaseManager) TerminateConnections(_ context.Context, _ pgmi.DBConnection, _ string) error {
	return m.terminateErr
}

type mockLogger struct{}

func (m *mockLogger) Verbose(_ string, _ ...any) {}
func (m *mockLogger) Info(_ string, _ ...any)    {}
func (m *mockLogger) Error(_ string, _ ...any)   {}
