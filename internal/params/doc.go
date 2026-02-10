// Package params provides session variable management for pgmi deployments.
//
// Parameters are stored in pg_temp.pgmi_parameter and automatically set
// as PostgreSQL session variables with the 'pgmi.' namespace prefix.
//
// # Session Variables
//
// CLI parameters are passed via --param flags and take precedence over
// defaults defined in deploy.sql via pgmi_declare_param().
//
// Access parameters at runtime using:
//   - current_setting('pgmi.key', true)        - Direct PostgreSQL function
//   - pg_temp.pgmi_get_param('key', 'default') - Convenience wrapper with fallback
//
// # PostgreSQL Utility Functions
//
// The CreateSchema function installs pg_temp functions that deploy.sql can use:
//   - pg_temp.pgmi_declare_param(): Declare parameters with type validation
//   - pg_temp.pgmi_get_param(): Get parameter value with fallback
//
// These functions are session-scoped (pg_temp schema) and disappear when the
// connection closes, ensuring clean separation between deployments.
//
// # Example Usage
//
//	// Create utility functions in pg_temp schema
//	if err := params.CreateSchema(ctx, pool); err != nil {
//	    return fmt.Errorf("failed to create schema: %w", err)
//	}
//
//	// In SQL (deploy.sql):
//	// SELECT pg_temp.pgmi_declare_param('env', p_default_value => 'development');
//	// SELECT current_setting('pgmi.env');  -- Returns: 'development'
//
// # Thread Safety
//
// All functions are safe for concurrent use.
package params
