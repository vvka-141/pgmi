package pgmi

import "time"

// DeployResult is what a deploy did, as far as it got.
type DeployResult struct {
	// FilesLoaded counts the project files loaded into the session.
	FilesLoaded int
	// Ignored lists the paths file discovery skipped.
	Ignored []IgnoredPath
	// TestMacros counts the CALL pgmi_test() macros expanded in deploy.sql.
	TestMacros int
	// Duration is the wall time of the deploy.
	Duration time.Duration
	// Database is the target database.
	Database string
	// ExecutionUnits counts the messages deploy.sql was sent as: the atomic
	// head, then one per top-level statement of the tail.
	ExecutionUnits int
	// UnitsCommitted counts the units whose work is committed.
	UnitsCommitted int
	// FailedUnit is the 1-based unit that failed, or 0.
	FailedUnit int
	// ExecutionMode is set on failure: "atomic" when nothing was committed,
	// "psql" when earlier tail units were.
	ExecutionMode string
	// ServerVersion is the PostgreSQL server_version.
	ServerVersion string
	// TLS is how the deploy session was encrypted; nil until it connected.
	TLS *TLSInfo
	// Created is true when this deploy created a database that did not exist,
	// which is also what a mistyped database name looks like.
	Created bool
	// Recreated is true when --overwrite dropped the target and created it
	// again: a failed deploy then leaves it empty, its old data gone.
	Recreated bool
}

// TLSInfo is the deploy session's transport: whether TLS was negotiated, its
// version, and the sslmode that allowed it.
type TLSInfo struct {
	Enabled bool
	Version string
	SSLMode string
}
