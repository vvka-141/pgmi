package pgmi_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestDeploymentConfig_FormatRedactsSecrets(t *testing.T) {
	for _, conn := range []string{
		"postgresql://admin:URISECRET@db.example:5432/app",
		"Host=db.example;Database=app;Username=admin;Password=ADOSECRET",
		"Host=db.example;Database=app;Username=admin;Pwd=ADOSECRET",
		"Host=db.example;Database=app;Username=admin;Password='ADO''SECRET'",
	} {
		cfg := pgmi.DeploymentConfig{
			ConnectionString:  conn,
			DatabaseName:      "app",
			AzureClientSecret: "AZSECRET",
			Parameters:        map[string]string{"database_admin_password": "PARAMSECRET", "env": "prod"},
		}
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			for _, value := range []any{cfg, &cfg} {
				out := fmt.Sprintf(verb, value)
				for _, secret := range []string{"URISECRET", "ADOSECRET", "SECRET'", "AZSECRET", "PARAMSECRET", "prod"} {
					if strings.Contains(out, secret) {
						t.Errorf("%s of %T shows %s: %s", verb, value, secret, out)
					}
				}
				if !strings.Contains(out, "database_admin_password") || !strings.Contains(out, "app") {
					t.Errorf("%s of %T must keep the non-secret fields and parameter keys: %s", verb, value, out)
				}
			}
		}
	}
}
