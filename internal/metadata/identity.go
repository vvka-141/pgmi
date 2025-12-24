package metadata

import (
	"strings"

	"github.com/google/uuid"
)

// NamespaceFileIdentity is the fixed UUID namespace for generating deterministic
// file identities from file paths. This namespace is derived from the string
// "pgmi.com/file-identity/v1" using UUID v5 with the URL namespace.
//
// Value: 6ba7b810-9dad-11d1-80b4-00c04fd430c8 (standard UUID v5 namespace for URLs)
//        hashed with "pgmi.com/file-identity/v1"
//
// This constant ensures that:
//   - File paths always generate the same UUID across deployments
//   - The namespace is unique to PGMI (no collisions with other systems)
//   - Users can independently verify deterministic ID generation
var NamespaceFileIdentity = uuid.MustParse("e8c72c3e-7b4a-5f9d-b8e1-4c6d8a2e5f7c")

// init generates the namespace UUID from the canonical string on package load.
// This is computed once at startup for efficiency.
func init() {
	// Generate namespace from URL namespace + canonical string
	// uuid.NameSpaceURL is the standard UUID v5 namespace for URL identifiers
	NamespaceFileIdentity = uuid.NewSHA1(uuid.NameSpaceURL, []byte("pgmi.com/file-identity/v1"))
}

// GenerateFallbackID creates a deterministic UUID v5 from a normalized file path.
// This is used for files without explicit <pgmi:meta> blocks to ensure stable
// identity across deployments and renames.
//
// Path Normalization:
//  1. Convert to lowercase (case-insensitive identity)
//  2. Ensure forward slashes (Unix-style paths, already done by scanner)
//  3. Remove leading "./" prefix (consistent root reference)
//
// Algorithm:
//   - Uses UUID v5 (SHA-1 based) for better cryptographic properties than v3 (MD5)
//   - Namespace: NamespaceFileIdentity (derived from "pgmi.com/file-identity/v1")
//   - Input: Normalized file path
//
// Examples:
//   - "./migrations/001_users.sql" → uuid_v5(namespace, "migrations/001_users.sql")
//   - "./SETUP/Schema.SQL" → uuid_v5(namespace, "setup/schema.sql")  (case-insensitive)
//
// Parameters:
//   - path: File path (typically Unix-style forward slashes from scanner)
//
// Returns:
//   - uuid.UUID: Deterministic UUID v5 for the given path
func GenerateFallbackID(path string) uuid.UUID {
	// Normalize path for consistent ID generation
	normalized := normalizePath(path)

	// Generate UUID v5 using SHA-1
	return uuid.NewSHA1(NamespaceFileIdentity, []byte(normalized))
}

// normalizePath converts a file path to canonical form for deterministic UUID generation.
//
// Transformations:
//  1. Lowercase (case-insensitive filesystems compatibility)
//  2. Remove leading "./" (consistent root reference)
//
// Note: Forward slashes are already enforced by the file scanner.
func normalizePath(path string) string {
	// Convert to lowercase
	normalized := strings.ToLower(path)

	// Remove leading "./" prefix
	normalized = strings.TrimPrefix(normalized, "./")

	return normalized
}

