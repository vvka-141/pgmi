package pgmi

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// DeploymentConfig contains all parameters needed for a deployment operation.
type DeploymentConfig struct {
	// SourcePath is the root directory containing deploy.sql and other SQL files
	SourcePath string

	// DatabaseName is the target database name
	DatabaseName string

	// MaintenanceDatabase is the database to connect to for server-level operations
	// (CREATE DATABASE, DROP DATABASE). Typically "postgres".
	MaintenanceDatabase string

	// ConnectionString is the PostgreSQL connection string (URI or ADO.NET format)
	// After CLI resolution, this contains the TARGET database connection
	ConnectionString string

	// Overwrite enables the destructive drop/recreate workflow
	Overwrite bool

	// Force bypasses interactive approval when used with Overwrite
	Force bool

	// Parameters are key-value pairs passed to pgmi_params table
	Parameters map[string]string

	// Timeout is the global timeout for the entire deployment
	Timeout time.Duration

	// Verbose enables detailed logging
	Verbose bool

	// AuthMethod indicates the authentication mechanism to use
	AuthMethod AuthMethod

	// Azure Entra ID authentication parameters (used when AuthMethod is AuthMethodAzureEntraID)
	AzureTenantID     string
	AzureClientID     string
	AzureClientSecret string
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

	// Force requires Overwrite to be set
	if c.Force && !c.Overwrite {
		errs = append(errs, fmt.Errorf("force flag requires overwrite to be enabled: %w", ErrInvalidConfig))
	}

	// Validate timeout if set
	if c.Timeout < 0 {
		errs = append(errs, fmt.Errorf("timeout cannot be negative: %w", ErrInvalidConfig))
	}

	return errors.Join(errs...)
}

// TestConfig contains all parameters needed for a test execution operation.
type TestConfig struct {
	// SourcePath is the root directory containing SQL files and test files
	SourcePath string

	// DatabaseName is the target database name (required)
	DatabaseName string

	// ConnectionString is the PostgreSQL connection string (URI or ADO.NET format)
	ConnectionString string

	// Timeout is the global timeout for the entire test execution
	Timeout time.Duration

	// FilterPattern is a POSIX regex to filter tests (default: ".*" matches all)
	FilterPattern string

	// ListOnly prints tests without executing them (dry-run mode)
	ListOnly bool

	// Parameters are key-value pairs for parameterized tests (optional)
	Parameters map[string]string

	// Verbose enables detailed logging
	Verbose bool

	// AuthMethod indicates the authentication mechanism to use
	AuthMethod AuthMethod

	// Azure Entra ID authentication parameters (used when AuthMethod is AuthMethodAzureEntraID)
	AzureTenantID     string
	AzureClientID     string
	AzureClientSecret string
}

// Validate checks if the TestConfig has all required fields and valid values.
// It returns a multi-error if multiple validation failures occur.
func (c *TestConfig) Validate() error {
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

	// FilterPattern defaults to ".*" (match all) if empty
	if c.FilterPattern == "" {
		c.FilterPattern = ".*"
	}

	return errors.Join(errs...)
}

// ConnectionConfig represents parsed connection parameters.
type ConnectionConfig struct {
	Host     string
	Port     int
	Database string
	Username string
	Password string
	SSLMode  string

	// AuthMethod indicates the authentication mechanism to use
	AuthMethod AuthMethod

	// Additional connection parameters
	AppName          string
	ConnectTimeout   time.Duration
	AdditionalParams map[string]string

	// Azure Entra ID authentication parameters (used when AuthMethod is AuthMethodAzureEntraID)
	// If all three are provided, Service Principal authentication is used.
	// If none are provided, DefaultAzureCredential chain is used (env vars, managed identity, CLI, etc.)
	AzureTenantID     string
	AzureClientID     string
	AzureClientSecret string
}

// AuthMethod represents the type of authentication to use.
type AuthMethod int

const (
	AuthMethodStandard AuthMethod = iota // Username/Password
	AuthMethodCertificate                // mTLS
	AuthMethodAWSIAM                     // AWS IAM Database Authentication
	AuthMethodGoogleIAM                  // Google Cloud SQL IAM
	AuthMethodAzureEntraID               // Azure Active Directory (Entra ID)
)

// String returns a human-readable string representation of the AuthMethod.
func (a AuthMethod) String() string {
	switch a {
	case AuthMethodStandard:
		return "Standard"
	case AuthMethodCertificate:
		return "Certificate"
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

// IsValid returns true if the AuthMethod is a valid, defined value.
func (a AuthMethod) IsValid() bool {
	return a >= AuthMethodStandard && a <= AuthMethodAzureEntraID
}

// FileMetadata represents a file loaded into pgmi_files table.
// All file paths use Unix-style forward slashes for cross-platform consistency.
type FileMetadata struct {
	// Path information (Unix forward slashes)
	Path      string // Relative path from source root: "migrations/001_users.sql"
	Name      string // Filename only: "001_users.sql"
	Directory string // Parent directory: "migrations" or "" for root
	Extension string // File extension: ".sql"
	Depth     int    // Nesting level (0 = root, 1 = first subdir, etc.)

	// Content
	Content string // Full unmodified file content

	// Size
	SizeBytes int64 // File size in bytes

	// Checksums
	Checksum    string // SHA-256 of NORMALIZED content (for idempotent tracking)
	ChecksumRaw string // SHA-256 of RAW content (for exact change detection)

	// Timestamps (using modified_at per MVP spec)
	ModifiedAt time.Time // Last modification time

	// Metadata (optional, only for files with valid <pgmi:meta> blocks)
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
//   Files can specify multiple sort keys to execute at different deployment stages.
//   Each key results in a separate execution entry in the plan.
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
