package scaffold_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/files/fakefs"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// The advanced template fills missing role passwords with 'postgres' when
// env=dev. It used to treat a missing env as dev too, so a production deploy
// whose params file forgot the admin password created a LOGIN role with
// password 'postgres' and exited 0. A deploy must now say its environment,
// and a non-dev one must supply the password.
func TestAdvancedTemplate_FailsClosedWithoutEnvOrPassword(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	efs := fakefs.NewEmbedFileSystem(scaffold.GetTemplatesFS(), "templates/advanced")

	for _, tc := range []struct {
		name   string
		params map[string]string
		want   string
	}{
		{"no env", map[string]string{"database_admin_password": "TestPassword123!"}, "env"},
		{"prod without password", map[string]string{"env": "prod"}, "database_admin_password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := "pgmi_env_required"
			defer testhelpers.CleanupTestDB(t, connString, db)
			err := testhelpers.NewTestDeployerWithFS(t, efs).Deploy(context.Background(), pgmi.DeploymentConfig{
				ConnectionString: connString,
				DatabaseName:     db,
				SourcePath:       ".",
				Overwrite:        true,
				Force:            true,
				Parameters:       tc.params,
			})
			if err == nil {
				t.Fatal("deploy succeeded; it must fail closed")
			}
			if !strings.Contains(err.Error(), "Missing required parameters") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want 'Missing required parameters' naming %s, got: %v", tc.want, err)
			}
		})
	}
}
