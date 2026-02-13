package contract

import (
	"context"
	_ "embed"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed api-v1.sql
var apiV1SQL string

// Version represents an API version identifier.
type Version string

const (
	V1     Version = "1"
	Latest Version = V1
)

var supportedVersions = map[Version]string{
	V1: "api-v1.sql",
}

// Load returns the SQL content for the specified API version.
// If version is empty, the latest version is used.
// Returns the SQL content, the resolved version, and any error.
func Load(version string) (string, Version, error) {
	v := Version(version)
	if v == "" {
		v = Latest
	}

	switch v {
	case V1:
		return apiV1SQL, v, nil
	default:
		return "", "", fmt.Errorf("unsupported API version %q; supported: %v", version, SupportedVersions())
	}
}

// Apply executes the API contract SQL for the specified version.
// This creates the public views and functions that deploy.sql depends on.
// Must be called after schema.sql and file loading.
func Apply(ctx context.Context, conn *pgxpool.Conn, version string) (Version, error) {
	sql, v, err := Load(version)
	if err != nil {
		return "", err
	}

	_, err = conn.Exec(ctx, sql)
	if err != nil {
		return "", fmt.Errorf("failed to apply API v%s: %w", v, err)
	}

	return v, nil
}

// SupportedVersions returns a sorted list of all supported API versions.
func SupportedVersions() []Version {
	versions := make([]Version, 0, len(supportedVersions))
	for v := range supportedVersions {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	return versions
}

// LatestVersion returns the current latest API version.
func LatestVersion() Version {
	return Latest
}
