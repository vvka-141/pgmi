// Package params provides session variable management for pgmi deployments.
//
// Parameters are stored in pg_temp._pgmi_parameter and automatically set
// as PostgreSQL session variables with the 'pgmi.' namespace prefix.
//
// # Session Variables
//
// CLI parameters are passed via --param flags and become available to deploy.sql:
//   - Via session variables: current_setting('pgmi.key', true)
//   - Via view: SELECT value FROM pg_temp.pgmi_parameter_view WHERE key = 'mykey'
//
// Templates are responsible for their own parameter handling patterns (defaults,
// validation, type coercion). pgmi provides the raw data; templates provide logic.
//
// # Example Usage
//
//	// Create utility functions in pg_temp schema
//	if err := params.CreateSchema(ctx, pool); err != nil {
//	    return fmt.Errorf("failed to create schema: %w", err)
//	}
//
//	// In SQL (deploy.sql):
//	// v_env := COALESCE(current_setting('pgmi.env', true), 'development');
//
// # Thread Safety
//
// All functions are safe for concurrent use.
package params
