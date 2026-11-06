package db

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// AzureServicePrincipalProvider acquires tokens using Service Principal credentials.
// This is the primary authentication method for CI/CD pipelines.
type AzureServicePrincipalProvider struct {
	tenantID   string
	clientID   string
	credential *azidentity.ClientSecretCredential
}

// NewAzureServicePrincipalProvider creates a token provider for Service Principal auth.
// All three parameters (tenantID, clientID, clientSecret) are required.
func NewAzureServicePrincipalProvider(tenantID, clientID, clientSecret string) (*AzureServicePrincipalProvider, error) {
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("azure service principal requires tenantID, clientID, and clientSecret: %w", pgmi.ErrInvalidConfig)
	}

	cred, err := azidentity.NewClientSecretCredential(tenantID, clientID, clientSecret, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure credential: %w", err)
	}

	return &AzureServicePrincipalProvider{
		tenantID:   tenantID,
		clientID:   clientID,
		credential: cred,
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

// AzureIdentityProvider acquires tokens through an azidentity chain.
// Without a client ID it is DefaultAzureCredential (environment, workload
// identity, managed identity, Azure CLI, ...), scoped to the tenant when one is
// given. With a client ID the caller named one identity, so the chain holds
// only the credentials that can be it: workload identity and the user-assigned
// managed identity with that ID; the Azure CLI's signed-in user is not that
// identity.
type AzureIdentityProvider struct {
	credential azcore.TokenCredential
	name       string
}

var (
	newDefaultAzureCredential = func(o *azidentity.DefaultAzureCredentialOptions) (azcore.TokenCredential, error) {
		return azidentity.NewDefaultAzureCredential(o)
	}
	newWorkloadIdentityCredential = func(o *azidentity.WorkloadIdentityCredentialOptions) (azcore.TokenCredential, error) {
		return azidentity.NewWorkloadIdentityCredential(o)
	}
	newManagedIdentityCredential = func(o *azidentity.ManagedIdentityCredentialOptions) (azcore.TokenCredential, error) {
		return azidentity.NewManagedIdentityCredential(o)
	}
)

// NewAzureIdentityProvider creates a provider for the resolved tenant
// and client IDs; either may be empty.
func NewAzureIdentityProvider(tenantID, clientID string) (*AzureIdentityProvider, error) {
	if clientID == "" {
		cred, err := newDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{TenantID: tenantID})
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure default credential: %w", err)
		}
		return &AzureIdentityProvider{credential: cred, name: fmt.Sprintf("AzureDefaultCredential(tenant=%s)", tenantID)}, nil
	}

	var sources []azcore.TokenCredential
	if wi, err := newWorkloadIdentityCredential(&azidentity.WorkloadIdentityCredentialOptions{ClientID: clientID, TenantID: tenantID}); err == nil {
		sources = append(sources, wi)
	}
	mi, err := newManagedIdentityCredential(&azidentity.ManagedIdentityCredentialOptions{ID: azidentity.ClientID(clientID)})
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure managed identity credential for client %s: %w", clientID, err)
	}
	sources = append(sources, mi)
	chain, err := azidentity.NewChainedTokenCredential(sources, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure credential chain: %w", err)
	}
	return &AzureIdentityProvider{credential: chain, name: fmt.Sprintf("AzureManagedIdentity(client=%s)", clientID)}, nil
}

func (p *AzureIdentityProvider) GetToken(ctx context.Context) (string, time.Time, error) {
	token, err := p.credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{AzurePostgreSQLScope},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("azure token acquisition failed: %w", err)
	}
	return token.Token, token.ExpiresOn, nil
}

func (p *AzureIdentityProvider) String() string {
	return p.name
}
