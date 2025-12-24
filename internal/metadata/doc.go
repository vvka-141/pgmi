// Package metadata provides XML metadata parsing and validation for PGMI deployment scripts.
//
// # Overview
//
// This package implements the PGMI metadata system, which allows SQL scripts to declare:
//   - Unique identity (UUID, path-independent)
//   - Idempotency flag (safe to rerun vs one-time only)
//   - Execution order control (sortKey)
//   - Explicit dependencies (ensure correct execution sequence)
//   - Group membership (logical collections for filtering/dependencies)
//
// # Metadata Format
//
// Metadata is embedded in SQL file comments using XML:
//
//	/*
//	<pgmi-meta
//	    id="550e8400-e29b-41d4-a716-446655440000"
//	    idempotent="true"
//	    sortKey="2025-11-13/001">
//
//	  <description>
//	    Create users table and seed admin account
//	  </description>
//
//	  <membership>
//	    <group id="a78c2401-4835-4b91-b5fa-9358b9b0c8"/>
//	  </membership>
//
//	  <dependency>
//	    <dependsOn id="15f6ae80-2a91-479c-9ed8-56c8cc686f85"/>
//	  </dependency>
//
//	</pgmi-meta>
//	*/
//
// # Validation Rules
//
// The XSD schema (schema.xsd) defines strict validation:
//   - id: Required UUID (regex validated)
//   - idempotent: Required boolean (true/false)
//   - sortKey: Required non-empty string
//   - description: Optional free-form text
//   - membership: Optional, must have ≥1 group if present
//   - dependency: Optional, must have ≥1 dependsOn if present
//
// # Fallback Identity
//
// Files without metadata receive a deterministic UUID v5 generated from their
// normalized path. This ensures stable identity even for legacy scripts.
//
// Example:
//
//	path: "./migrations/001_users.sql"
//	fallback ID: uuid_v5(namespace, "migrations/001_users.sql")
//
// # Usage
//
// Extract and validate metadata from SQL file content:
//
//	meta, err := metadata.ExtractAndValidate(content, filePath)
//	if errors.Is(err, metadata.ErrNoMetadata) {
//	    // File has no metadata - use fallback ID
//	    fallbackID := metadata.GenerateFallbackID(filePath)
//	} else if err != nil {
//	    // Invalid metadata - fail deployment
//	    return err
//	}
//
// # Package Structure
//
//   - types.go: Go structs mapping XSD schema
//   - schema.xsd: Canonical XSD schema (embedded via go:embed)
//   - extractor.go: XML parsing from SQL comments
//   - validator.go: XSD constraint validation
//   - identity.go: Deterministic UUID v5 fallback generation
//
// # Design Principles
//
//  1. Fail Fast: Invalid metadata is caught during file scanning (before DB session)
//  2. Optional but Validated: Metadata is optional, but if present, must be valid
//  3. Deterministic: File paths always generate the same fallback UUID
//  4. Pure Go: No external dependencies (uses stdlib encoding/xml)
//  5. XSD-Compliant: Strict adherence to schema.xsd specification
package metadata
