// Package loader handles loading file metadata and parameters into PostgreSQL session-scoped tables.
//
// The loader package is responsible for:
//   - Loading file metadata into pg_temp._pgmi_source
//   - Loading CLI parameters into pg_temp._pgmi_parameter
//   - Using batch operations for efficient database loading
//
// All tables are created in the pg_temp schema (session-scoped temporary tables),
// ensuring they are automatically cleaned up when the database session ends.
package loader
