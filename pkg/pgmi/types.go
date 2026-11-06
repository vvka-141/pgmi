package pgmi

import (
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// DeploymentConfig contains all parameters needed for a deployment operation.
type DeploymentConfig struct {
	// SourcePath is the root directory containing deploy.sql and other SQL files
	SourcePath string

	// DatabaseName is the target database name
	DatabaseName string

	// MaintenanceDatabase is the database to connect to for server-level operations
	// (CREATE DATABASE, DROP DATABASE). Empty means "postgres".
	MaintenanceDatabase string

	// ConnectionString is the server and login, as a PostgreSQL URI, keyword/value
	// or ADO.NET string. Its database name plays no part: DatabaseName is the
	// target and MaintenanceDatabase the maintenance database.
	ConnectionString string

	// Overwrite enables the destructive drop/recreate workflow
	Overwrite bool

	// Force bypasses interactive approval prompts; a no-op when no prompt would occur
	Force bool

	// Parameters are key-value pairs passed to pgmi_params table
	Parameters map[string]string

	// Compat specifies the pgmi session compatibility level.
	// Empty string means use the latest version.
	Compat string

	// Verbose enables detailed logging
	Verbose bool

	// AuthMethod indicates the authentication mechanism to use
	AuthMethod AuthMethod

	// Azure Entra ID authentication parameters (used when AuthMethod is AuthMethodAzureEntraID)
	AzureTenantID     string
	AzureClientID     string
	AzureClientSecret string

	AWSRegion      string
	GoogleInstance string
}

// String shows the configuration without its secrets: the connection
// string's password redacted, the Azure client secret as set or empty, and
// the parameters by key only, since a parameter may carry a secret.
func (c DeploymentConfig) String() string {
	secret := "empty"
	if c.AzureClientSecret != "" {
		secret = "set"
	}
	keys := make([]string, 0, len(c.Parameters))
	for k := range c.Parameters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("{SourcePath:%s DatabaseName:%s MaintenanceDatabase:%s ConnectionString:%s Overwrite:%t Force:%t "+
		"Parameters:%v Compat:%s Verbose:%t AuthMethod:%v AzureTenantID:%s AzureClientID:%s AzureClientSecret:%s "+
		"AWSRegion:%s GoogleInstance:%s}",
		c.SourcePath, c.DatabaseName, c.MaintenanceDatabase, redactPasswords(c.ConnectionString), c.Overwrite, c.Force,
		keys, c.Compat, c.Verbose, c.AuthMethod, c.AzureTenantID, c.AzureClientID, secret, c.AWSRegion, c.GoogleInstance)
}

// GoString is String for %#v.
func (c DeploymentConfig) GoString() string {
	return "pgmi.DeploymentConfig" + c.String()
}

// Validate checks if the DeploymentConfig has all required fields and valid values.
// It returns a multi-error if multiple validation failures occur.
func (c *DeploymentConfig) Validate() error {
	var errs []error

	if c.SourcePath == "" {
		errs = append(errs, fmt.Errorf("SourcePath is required: %w", ErrInvalidConfig))
	}

	if c.DatabaseName == "" {
		errs = append(errs, fmt.Errorf("DatabaseName is required: %w", ErrInvalidConfig))
	}

	if c.ConnectionString == "" {
		errs = append(errs, fmt.Errorf("ConnectionString is required: %w", ErrInvalidConfig))
	}

	return errors.Join(errs...)
}

// AuthMethod represents the type of authentication to use.
type AuthMethod int

const (
	AuthMethodStandard     AuthMethod = iota // Username/Password
	AuthMethodAWSIAM                         // AWS IAM Database Authentication
	AuthMethodGoogleIAM                      // Google Cloud SQL IAM
	AuthMethodAzureEntraID                   // Azure Active Directory (Entra ID)
)

// String returns a human-readable string representation of the AuthMethod.
func (a AuthMethod) String() string {
	switch a {
	case AuthMethodStandard:
		return "Standard"
	case AuthMethodAWSIAM:
		return "AWS IAM"
	case AuthMethodGoogleIAM:
		return "Google IAM"
	case AuthMethodAzureEntraID:
		return "Azure Entra ID"
	default:
		return fmt.Sprintf("Unknown(%d)", a)
	}
}

// FileMetadata represents a file loaded into pgmi_files table.
// All file paths use Unix-style forward slashes for cross-platform consistency.
type FileMetadata struct {
	// Path information (Unix forward slashes)
	Path      string // Relative path from source root: "migrations/001_users.sql"
	Directory string // Parent directory: "migrations" or "" for root
	Extension string // File extension: ".sql"

	// Content
	Content string // Full unmodified file content

	// Checksums
	Checksum    string // SHA-256 of NORMALIZED content (for idempotent tracking)
	ChecksumRaw string // SHA-256 of RAW content (for exact change detection)

	// Metadata (optional, only for files with valid <pgmi-meta> blocks)
	// If nil, the file has no metadata and will use a deterministic fallback UUID.
	Metadata *ScriptMetadata
}

// ScriptMetadata represents parsed and validated metadata from a <pgmi-meta> XML block.
// This is the public-facing type used throughout the application, converted from
// the internal metadata.Metadata type during file scanning.
//
// Purpose:
//   - ID: Unique identifier for path-independent tracking
//   - Idempotent: Whether the script can be safely rerun
//   - SortKeys: Array of execution keys enabling multi-phase execution
//   - Description: Human-readable purpose of the script
//
// Multi-Phase Execution:
//
//	Files can specify multiple sort keys to execute at different deployment stages.
//	Each key results in a separate execution entry in the plan.
//
// Example usage:
//
//	if file.Metadata != nil {
//	    fmt.Printf("Script %s (ID: %s, idempotent: %v, phases: %d)\n",
//	        file.Path, file.Metadata.ID, file.Metadata.Idempotent, len(file.Metadata.SortKeys))
//	}
type ScriptMetadata struct {
	// ID is the globally unique identifier for this script.
	// Used for tracking execution independent of file path (enables renames).
	ID uuid.UUID

	// Idempotent indicates whether the script can be safely rerun.
	// true: Script can execute multiple times (e.g., CREATE OR REPLACE functions)
	// false: Script should execute only once (e.g., one-time data migrations)
	Idempotent bool

	// SortKeys is an array of execution keys for multi-phase execution.
	// Format convention: "phase/sequence" (e.g., "10-utils/0010", "30-core/2000")
	// Each key results in a separate execution at that position in the deployment.
	// Scripts are ordered by: sort_key ASC, path ASC (deterministic)
	SortKeys []string

	// Description is a human-readable explanation of what the script does.
	// Optional, but highly recommended for maintainability.
	Description string
}
