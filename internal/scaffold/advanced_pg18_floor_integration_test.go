package scaffold_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vvka-141/pgmi/internal/files/fakefs"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestAdvancedTemplate_RefusesServerBelowPostgres18(t *testing.T) {
	connString := testhelpers.RequirePostgresImage(t, "postgres:17")
	efs := fakefs.NewEmbedFileSystem(scaffold.GetTemplatesFS(), "templates/advanced")
	db := "pgmi_pg18_floor"

	err := testhelpers.NewTestDeployerWithFS(t, efs).Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString: connString,
		DatabaseName:     db,
		SourcePath:       ".",
		Overwrite:        true,
		Force:            true,
		Parameters:       map[string]string{"env": "dev"},
	})
	if err == nil {
		t.Fatal("deploy on PostgreSQL 17 succeeded; the advanced template must refuse it")
	}
	if !strings.Contains(err.Error(), "requires PostgreSQL 18 or newer") || !strings.Contains(err.Error(), "17.") {
		t.Fatalf("want a refusal naming the found version and the 18 requirement, got: %v", err)
	}

	conn, err := pgx.Connect(context.Background(), strings.Replace(connString, "/postgres?", "/"+db+"?", 1))
	if err != nil {
		t.Fatalf("connect to %s: %v", db, err)
	}
	defer conn.Close(context.Background())
	var leaked []string
	if err := conn.QueryRow(context.Background(), `
		SELECT coalesce(array_agg(name ORDER BY name), '{}')
		FROM (
			SELECT nspname AS name FROM pg_namespace WHERE nspname IN ('internal', 'core', 'api', 'common', 'membership', 'extensions')
			UNION ALL
			SELECT rolname FROM pg_roles WHERE rolname LIKE $1 || '\_%'
		) s`, db).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if len(leaked) > 0 {
		t.Fatalf("refused deploy left objects behind: %v", leaked)
	}
}
