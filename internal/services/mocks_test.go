package services

import (
	"context"

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

type mockFileScanner struct {
	scanResult pgmi.FileScanResult
	scanErr    error
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
}

func (m *mockDatabaseManager) Exists(_ context.Context, _ pgmi.DBConnection, _ string) (bool, error) {
	return m.existsResult, m.existsErr
}

func (m *mockDatabaseManager) Create(_ context.Context, _ pgmi.DBConnection, _ string) error {
	return m.createErr
}

func (m *mockDatabaseManager) Drop(_ context.Context, _ pgmi.DBConnection, _ string) error {
	return m.dropErr
}

func (m *mockDatabaseManager) TerminateConnections(_ context.Context, _ pgmi.DBConnection, _ string) error {
	return m.terminateErr
}

type mockLogger struct{}

func (m *mockLogger) Verbose(_ string, _ ...interface{}) {}
func (m *mockLogger) Info(_ string, _ ...interface{})    {}
func (m *mockLogger) Error(_ string, _ ...interface{})   {}
