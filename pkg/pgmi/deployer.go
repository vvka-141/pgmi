package pgmi

import "context"

// Deployer is the main interface for executing database deployments.
type Deployer interface {
	Deploy(ctx context.Context, config DeploymentConfig) error
}
