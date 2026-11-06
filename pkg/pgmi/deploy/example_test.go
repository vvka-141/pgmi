package deploy_test

import (
	"context"
	"fmt"
	"os"

	"github.com/vvka-141/pgmi/pkg/pgmi"
	"github.com/vvka-141/pgmi/pkg/pgmi/deploy"
)

func Example() {
	result, err := deploy.New().Deploy(context.Background(), pgmi.DeploymentConfig{
		SourcePath:          "./db",
		ConnectionString:    os.Getenv("DATABASE_URL"),
		MaintenanceDatabase: "postgres",
		DatabaseName:        "app",
		Parameters:          map[string]string{"env": "dev"},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, pgmi.FormatError(err))
		os.Exit(pgmi.ExitCodeForError(err))
	}
	fmt.Printf("deployed %d files to %s in %s\n", result.FilesLoaded, result.Database, result.Duration)
}
