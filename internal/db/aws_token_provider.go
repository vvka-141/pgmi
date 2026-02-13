package db

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
)

// AWSIAMTokenProvider acquires IAM authentication tokens for RDS.
// Uses the default AWS credential chain (environment variables, config files, IAM roles, etc.)
type AWSIAMTokenProvider struct {
	endpoint string // host:port
	region   string
	username string
}

// NewAWSIAMTokenProvider creates a token provider for AWS RDS IAM authentication.
// endpoint is the RDS endpoint in host:port format (e.g., "mydb.cluster.region.rds.amazonaws.com:5432").
// region is the AWS region (e.g., "us-west-2").
// username is the database user configured for IAM authentication.
func NewAWSIAMTokenProvider(endpoint, region, username string) (*AWSIAMTokenProvider, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("AWS IAM auth requires endpoint (host:port)")
	}
	if region == "" {
		return nil, fmt.Errorf("AWS IAM auth requires region (use --aws-region or $AWS_REGION)")
	}
	if username == "" {
		return nil, fmt.Errorf("AWS IAM auth requires database username")
	}

	return &AWSIAMTokenProvider{
		endpoint: endpoint,
		region:   region,
		username: username,
	}, nil
}

// GetToken acquires an IAM authentication token from AWS.
// The token is valid for 15 minutes from acquisition time.
func (p *AWSIAMTokenProvider) GetToken(ctx context.Context) (string, time.Time, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(p.region))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	token, err := auth.BuildAuthToken(ctx, p.endpoint, p.region, p.username, cfg.Credentials)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to build RDS auth token: %w", err)
	}

	// RDS IAM tokens are valid for 15 minutes
	expiresOn := time.Now().Add(15 * time.Minute)

	return token, expiresOn, nil
}

// String returns a human-readable representation of the provider.
func (p *AWSIAMTokenProvider) String() string {
	return fmt.Sprintf("AWSIAMTokenProvider(endpoint=%s, region=%s, user=%s)", p.endpoint, p.region, p.username)
}
