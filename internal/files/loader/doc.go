// Package loader handles loading file metadata and parameters into PostgreSQL session-scoped tables.
//
// The loader package is responsible for:
//   - Creating pg_temp.files table and loading file metadata
//   - Creating pg_temp.params table and loading CLI parameters
//   - Using batch operations for efficient database loading
//
// All tables are created in the pg_temp schema (session-scoped temporary tables),
// ensuring they are automatically cleaned up when the database session ends.
package loader
