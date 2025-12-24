package db

import (
	"context"
	"time"
)

// TokenProvider abstracts cloud token acquisition for database authentication.
// This interface enables testability (mock providers) and future extensibility
// (AWS IAM, GCP, etc. can implement the same interface).
type TokenProvider interface {
	// GetToken acquires an OAuth token for database authentication.
	// The token is used as the password when connecting to cloud-hosted PostgreSQL.
	// Returns the token string and its expiry time.
	GetToken(ctx context.Context) (token string, expiresOn time.Time, err error)

	// String returns a human-readable description for logging.
	// Should NOT include secrets. Example: "AzureServicePrincipal(tenant=xxx, client=yyy)"
	String() string
}

// AzurePostgreSQLScope is the OAuth scope for Azure Database for PostgreSQL.
// This is the resource identifier that Azure AD uses to issue tokens for PostgreSQL access.
const AzurePostgreSQLScope = "https://ossrdbms-aad.database.windows.net/.default"
