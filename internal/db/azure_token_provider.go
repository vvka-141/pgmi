package db

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// AzureServicePrincipalProvider acquires tokens using Service Principal credentials.
// This is the primary authentication method for CI/CD pipelines.
type AzureServicePrincipalProvider struct {
	tenantID     string
	clientID     string
	clientSecret string
	credential   *azidentity.ClientSecretCredential
}

// NewAzureServicePrincipalProvider creates a token provider for Service Principal auth.
// All three parameters (tenantID, clientID, clientSecret) are required.
func NewAzureServicePrincipalProvider(tenantID, clientID, clientSecret string) (*AzureServicePrincipalProvider, error) {
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("azure service principal requires tenantID, clientID, and clientSecret")
	}

	cred, err := azidentity.NewClientSecretCredential(tenantID, clientID, clientSecret, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure credential: %w", err)
	}

	return &AzureServicePrincipalProvider{
		tenantID:     tenantID,
		clientID:     clientID,
		clientSecret: clientSecret,
		credential:   cred,
	}, nil
}

func (p *AzureServicePrincipalProvider) GetToken(ctx context.Context) (string, time.Time, error) {
	token, err := p.credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{AzurePostgreSQLScope},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("azure token acquisition failed: %w", err)
	}
	return token.Token, token.ExpiresOn, nil
}

func (p *AzureServicePrincipalProvider) String() string {
	return fmt.Sprintf("AzureServicePrincipal(tenant=%s, client=%s)", p.tenantID, p.clientID)
}

// AzureDefaultCredentialProvider uses Azure's DefaultAzureCredential chain.
// This automatically tries multiple authentication methods in order:
// 1. Environment variables (AZURE_CLIENT_ID, AZURE_CLIENT_SECRET, AZURE_TENANT_ID)
// 2. Workload Identity (for Kubernetes)
// 3. Managed Identity (for Azure VMs, App Service, etc.)
// 4. Azure CLI (for local development)
// 5. Azure Developer CLI
// 6. Azure PowerShell
type AzureDefaultCredentialProvider struct {
	credential azcore.TokenCredential
}

// NewAzureDefaultCredentialProvider creates a provider using the default credential chain.
func NewAzureDefaultCredentialProvider() (*AzureDefaultCredentialProvider, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure default credential: %w", err)
	}

	return &AzureDefaultCredentialProvider{
		credential: cred,
	}, nil
}

func (p *AzureDefaultCredentialProvider) GetToken(ctx context.Context) (string, time.Time, error) {
	token, err := p.credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{AzurePostgreSQLScope},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("azure token acquisition failed: %w", err)
	}
	return token.Token, token.ExpiresOn, nil
}

func (p *AzureDefaultCredentialProvider) String() string {
	return "AzureDefaultCredential"
}
