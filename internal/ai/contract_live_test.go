package ai_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/vvka-141/pgmi/internal/contract"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// The source-parsing drift test can only see views backed by a CREATE TABLE,
// so pgmi_plan_view, derived by a JOIN, was compared against nothing. Reading
// the columns PostgreSQL actually built covers every published view, in order.
func TestContract_LiveViewColumnsMatch(t *testing.T) {
	connStr := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if _, err := contract.Apply(ctx, conn, ""); err != nil {
		t.Fatalf("contract.Apply: %v", err)
	}

	for _, v := range ai.GetContract().Views {
		t.Run(v.Name, func(t *testing.T) {
			var cols []string
			err := conn.QueryRow(ctx, `
				SELECT coalesce(array_agg(a.attname::text ORDER BY a.attnum), '{}')
				FROM pg_attribute a
				WHERE a.attrelid = to_regclass('pg_temp.' || $1)
				  AND a.attnum > 0 AND NOT a.attisdropped`, v.Name).Scan(&cols)
			if err != nil {
				t.Fatalf("read columns: %v", err)
			}
			if !slices.Equal(cols, v.Columns) {
				t.Errorf("view %s: session has %v, contract publishes %v", v.Name, cols, v.Columns)
			}
		})
	}
}
