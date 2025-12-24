package params

import (
	"context"
	"os"
	"strings"
	"testing"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSchemaStatementByStatement(t *testing.T) {
	// This is a debug test that requires manual setup of pg_temp tables.
	// Skip unless explicitly enabled via PGMI_DEBUG_SCHEMA_TEST=1
	if os.Getenv("PGMI_DEBUG_SCHEMA_TEST") != "1" {
		t.Skip("Debug test: set PGMI_DEBUG_SCHEMA_TEST=1 to enable")
	}

	connStr := os.Getenv("PGMI_TEST_CONN")
	if connStr == "" {
		t.Skip("PGMI_TEST_CONN not set")
	}
	
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	
	// Split on semicolons outside dollar-quoted strings (simple heuristic)
	// This won't be perfect but should help find the problematic statement
	statements := strings.Split(schemaSQL, ";\n")
	
	for i, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			continue
		}
		
		// Add semicolon back
		fullStmt := stmt + ";"
		
		_, err := conn.Exec(ctx, fullStmt)
		if err != nil {
			t.Logf("Statement %d failed: %v", i, err)
			t.Logf("Statement (first 200 chars): %s", fullStmt[:min(200, len(fullStmt))])
			t.FailNow()
		}
	}
	t.Log("All statements executed successfully")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
