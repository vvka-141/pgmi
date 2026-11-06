package cli

import (
	"cmp"
	"fmt"
	"os"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// connectionStringFromEnv returns the first non-empty connection string from
// PGMI_CONNECTION_STRING or DATABASE_URL environment variables.
func connectionStringFromEnv() string {
	if s := os.Getenv("PGMI_CONNECTION_STRING"); s != "" {
		return s
	}
	return os.Getenv("DATABASE_URL")
}

// hasEnvConnectionSource returns true if environment variables provide enough
// connection info to skip the interactive wizard.
func hasEnvConnectionSource() bool {
	if connectionStringFromEnv() != "" {
		return true
	}
	return os.Getenv("PGHOST") != "" && os.Getenv("PGDATABASE") != ""
}

// resolveTargetDatabase resolves the deployment target: the -d/--database flag
// always outranks the database in the connection string.
func resolveTargetDatabase(flagDatabase, connConfigDatabase, yamlDatabase string, verbose bool) (string, error) {
	targetDB := flagDatabase

	if targetDB != "" {
		if verbose && connConfigDatabase != "" && targetDB != connConfigDatabase {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Using --database flag (%s) instead of connection string database (%s)\n",
				targetDB, connConfigDatabase)
		}
	} else {
		targetDB = cmp.Or(connConfigDatabase, yamlDatabase)
	}

	// Every line here must run as written: <project_path> is positional and
	// required, so omitting it fails at argument validation (exit 2) before
	// the connection string is ever read.
	if targetDB == "" {
		return "", fmt.Errorf("%w: database name is required\n"+
			"Provide via:\n"+
			"  1. --database/-d flag: pgmi deploy . -d mydb\n"+
			"  2. Connection string: pgmi deploy . --connection \"postgresql://user@host/mydb\"\n"+
			"  3. pgmi.yaml: connection.database: mydb\n"+
			"  4. Environment variable: export PGDATABASE=mydb, or a database in PGMI_CONNECTION_STRING / DATABASE_URL",
			pgmi.ErrInvalidConfig)
	}

	return targetDB, nil
}

// determineMaintenanceDB returns the maintenance database for CREATE DATABASE
// and --overwrite: currentMaintenanceDB, the resolver's, unless the connection
// came from a connection string whose database is the target because no -d
// was given; then 'postgres'.
func determineMaintenanceDB(
	fromConnectionString bool,
	flagDatabase string,
	connStringDatabase string,
	currentMaintenanceDB string,
) string {
	// Without -d, a connection string's database is the target, so the
	// maintenance database is 'postgres'. Flags, PG* variables and pgmi.yaml
	// name the maintenance database themselves.
	if fromConnectionString && flagDatabase == "" && connStringDatabase != "" && connStringDatabase != "postgres" {
		return "postgres"
	}
	return currentMaintenanceDB
}
