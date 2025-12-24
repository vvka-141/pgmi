package pgmi

import "context"

// Deployer is the main interface for executing database deployments.
// Implementations handle the full deployment workflow including connection,
// database preparation, file loading, and deploy.sql execution.
type Deployer interface {
	// Deploy executes a deployment using the provided configuration.
	// It returns an error if the deployment fails at any stage.
	Deploy(ctx context.Context, config DeploymentConfig) error
}

// Tester is the interface for executing database tests.
// Implementations handle session initialization and test execution from
// pg_temp.pgmi_unittest_plan without modifying database schema.
type Tester interface {
	// ExecuteTests runs tests using the provided configuration.
	// It returns an error if any test fails or if initialization fails.
	// Tests are executed in the order defined by pg_temp.pgmi_unittest_plan,
	// filtered by the provided pattern. Execution stops on first failure.
	ExecuteTests(ctx context.Context, config TestConfig) error
}
