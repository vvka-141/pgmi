package cli

import (
	"fmt"
	"os"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/db"
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

// resolveConnection consolidates connection resolution logic for both deploy and test commands.
// It handles connection string flags, granular flags, Azure/AWS/Google flags, and environment variables.
//
// Returns:
//   - ConnectionConfig with all parameters resolved
//   - Maintenance database name (for CREATE DATABASE operations)
//   - Error if configuration is invalid or conflicting
func resolveConnection(
	connStringFlag string,
	granularFlags *db.GranularConnFlags,
	azureFlags *db.AzureFlags,
	awsFlags *db.AWSFlags,
	googleFlags *db.GoogleFlags,
	certFlags *db.CertFlags,
	projectConfig *config.ProjectConfig,
) (*pgmi.ConnectionConfig, string, error) {
	connString := connStringFlag
	if connString == "" {
		connString = connectionStringFromEnv()
	}

	envVars := db.LoadFromEnvironment()

	connConfig, maintenanceDB, err := db.ResolveConnectionParams(
		connString,
		granularFlags,
		azureFlags,
		awsFlags,
		googleFlags,
		certFlags,
		envVars,
		projectConfig,
	)
	if err != nil {
		return nil, "", err
	}

	return connConfig, maintenanceDB, nil
}

// resolveTargetDatabase resolves the deployment target: the -d/--database flag
// always outranks the database in the connection string.
func resolveTargetDatabase(flagDatabase, connConfigDatabase string, verbose bool) (string, error) {
	targetDB := flagDatabase

	if targetDB != "" {
		if verbose && connConfigDatabase != "" && targetDB != connConfigDatabase {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Using --database flag (%s) instead of connection string database (%s)\n",
				targetDB, connConfigDatabase)
		}
	} else {
		targetDB = connConfigDatabase
	}

	// Every line here must run as written: <project_path> is positional and
	// required, so omitting it fails at argument validation (exit 2) before
	// the connection string is ever read.
	if targetDB == "" {
		return "", fmt.Errorf("%w: database name is required\n"+
			"Provide via:\n"+
			"  1. --database/-d flag: pgmi deploy . -d mydb\n"+
			"  2. Connection string: pgmi deploy . --connection \"postgresql://user@host/mydb\"\n"+
			"  3. Environment variable: export PGDATABASE=mydb",
			pgmi.ErrInvalidConfig)
	}

	return targetDB, nil
}

// determineMaintenanceDB determines the maintenance database for CREATE DATABASE operations.
// When the database comes from the connection string (not -d flag) and it's not 'postgres',
// we need to use 'postgres' as the maintenance DB for CREATE DATABASE operations.
//
// Parameters:
//   - flagDatabase: Database from -d flag (empty string if not provided)
//   - connStringDatabase: Database from connection string
//   - currentMaintenanceDB: Current maintenance DB from resolver
//
// Returns:
//   - Corrected maintenance database name
func determineMaintenanceDB(
	flagDatabase string,
	connStringDatabase string,
	currentMaintenanceDB string,
) string {
	// When database comes from connection string (not -d flag),
	// AND it's not 'postgres', we need to update maintenanceDB
	// to use 'postgres' for CREATE DATABASE operations
	if flagDatabase == "" && connStringDatabase != "" && connStringDatabase != "postgres" {
		return "postgres"
	}
	return currentMaintenanceDB
}
