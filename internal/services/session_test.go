package services

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestSession_Close_Idempotent verifies that calling Close() multiple times is safe
func TestSession_Close_Idempotent(t *testing.T) {
	// We can't easily mock pgxpool.Pool and pgxpool.Conn without a real database,
	// so this is more of a unit test for the Session struct logic
	// In reality, Close() will be tested via integration tests

	// Create a minimal session (nil pool/conn won't crash Close())
	session := &pgmi.Session{}

	// First close
	err := session.Close()
	if err != nil {
		t.Errorf("First Close() failed: %v", err)
	}

	// Second close (should be idempotent)
	err = session.Close()
	if err != nil {
		t.Errorf("Second Close() failed: %v", err)
	}

	// Third close (verify continued idempotence)
	err = session.Close()
	if err != nil {
		t.Errorf("Third Close() failed: %v", err)
	}
}

// TestSession_Accessors verifies the accessor methods work correctly
func TestSession_Accessors(t *testing.T) {
	// Note: This is primarily testing the struct design, not actual functionality
	// Real integration tests will test with actual database connections

	// We can create a session with nil pool/conn for structure testing
	// In production, NewSession will panic on nil, but for testing accessors
	// we can bypass the constructor
	session := &pgmi.Session{}

	// Test that nil accessors don't crash
	if session.Pool() != nil {
		t.Error("Expected nil pool")
	}
	if session.Conn() != nil {
		t.Error("Expected nil conn")
	}

	// Note: Can't easily test with real pool/conn without integration test setup
	t.Log("Session accessor structure test complete")
}

// TestNewSession_PanicsOnNilPool verifies that NewSession panics on nil pool
func TestNewSession_PanicsOnNilPool(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when pool is nil")
		}
	}()

	// This should panic
	pgmi.NewSession(nil, &pgxpool.Conn{}, pgmi.FileScanResult{})
}

// TestNewSession_PanicsOnNilConn verifies that NewSession panics on nil connection
func TestNewSession_PanicsOnNilConn(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when conn is nil")
		}
	}()

	// This should panic
	pgmi.NewSession(&pgxpool.Pool{}, nil, pgmi.FileScanResult{})
}

// TestSessionManager_PrepareSession_ErrorPaths tests error handling in session preparation
// This is a unit test focusing on error propagation
func TestSessionManager_PrepareSession_ErrorHandling(t *testing.T) {
	// These tests verify that errors are properly wrapped and returned
	// Actual integration tests with real database will test full functionality

	t.Run("missing source path returns error", func(t *testing.T) {
		// This would normally be caught by file scanner
		// Just verifying error propagation structure
		t.Skip("Requires integration test setup with mock components")
	})

	t.Run("invalid connection config returns error", func(t *testing.T) {
		// This would normally be caught during connection
		// Just verifying error propagation structure
		t.Skip("Requires integration test setup with mock components")
	})
}

// Note: Most session cleanup tests require integration test setup with actual
// PostgreSQL database. The critical tests are:
// 1. Session cleanup on error during file loading
// 2. Session cleanup on error during connection acquire
// 3. Context cancellation during session preparation
//
// These are better tested as integration tests in deployer_integration_test.go
// where we have access to real database connections.
