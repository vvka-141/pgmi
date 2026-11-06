package db

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type namedCredential string

func (c namedCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: string(c)}, nil
}

type azureCalls struct {
	defaultOpts  *azidentity.DefaultAzureCredentialOptions
	workloadOpts *azidentity.WorkloadIdentityCredentialOptions
	managedOpts  *azidentity.ManagedIdentityCredentialOptions
}

func stubAzureCredentials(t *testing.T, workloadErr error) *azureCalls {
	t.Helper()
	calls := &azureCalls{}
	origDefault, origWorkload, origManaged := newDefaultAzureCredential, newWorkloadIdentityCredential, newManagedIdentityCredential
	t.Cleanup(func() {
		newDefaultAzureCredential, newWorkloadIdentityCredential, newManagedIdentityCredential = origDefault, origWorkload, origManaged
	})
	newDefaultAzureCredential = func(o *azidentity.DefaultAzureCredentialOptions) (azcore.TokenCredential, error) {
		calls.defaultOpts = o
		return namedCredential("default"), nil
	}
	newWorkloadIdentityCredential = func(o *azidentity.WorkloadIdentityCredentialOptions) (azcore.TokenCredential, error) {
		calls.workloadOpts = o
		if workloadErr != nil {
			return nil, workloadErr
		}
		return namedCredential("workload"), nil
	}
	newManagedIdentityCredential = func(o *azidentity.ManagedIdentityCredentialOptions) (azcore.TokenCredential, error) {
		calls.managedOpts = o
		return namedCredential("managed"), nil
	}
	return calls
}

func TestNewAzureConnector_ClientIDWithoutSecretUsesThatIdentity(t *testing.T) {
	for name, workloadErr := range map[string]error{
		"on AKS with workload identity":   nil,
		"without the workload token file": errors.New("no token file specified"),
	} {
		t.Run(name, func(t *testing.T) {
			calls := stubAzureCredentials(t, workloadErr)
			p, err := NewAzureIdentityProvider("tenant-1", "client-uami")
			if err != nil {
				t.Fatal(err)
			}
			if calls.defaultOpts != nil {
				t.Error("a named client ID must not fall back to DefaultAzureCredential, which ignores it")
			}
			if calls.managedOpts == nil || calls.managedOpts.ID != azidentity.ClientID("client-uami") {
				t.Errorf("managed identity options = %+v, want ID ClientID(client-uami)", calls.managedOpts)
			}
			if calls.workloadOpts == nil || calls.workloadOpts.ClientID != "client-uami" || calls.workloadOpts.TenantID != "tenant-1" {
				t.Errorf("workload identity options = %+v, want client-uami in tenant-1", calls.workloadOpts)
			}
			if p.String() != "AzureManagedIdentity(client=client-uami)" {
				t.Errorf("String() = %q", p.String())
			}
			token, _, err := p.GetToken(context.Background())
			want := "workload"
			if workloadErr != nil {
				want = "managed"
			}
			if err != nil || token != want {
				t.Errorf("token %q, %v; want the %s credential first", token, err, want)
			}
		})
	}
}

func TestNewAzureIdentityProvider_DefaultChain(t *testing.T) {
	for _, tenant := range []string{"tenant-1", ""} {
		calls := stubAzureCredentials(t, nil)
		if _, err := NewAzureIdentityProvider(tenant, ""); err != nil {
			t.Fatal(err)
		}
		if calls.defaultOpts == nil || calls.defaultOpts.TenantID != tenant {
			t.Errorf("tenant %q: default chain options = %+v", tenant, calls.defaultOpts)
		}
		if calls.managedOpts != nil || calls.workloadOpts != nil {
			t.Errorf("tenant %q: no client ID must leave the default chain alone", tenant)
		}
	}
}

func TestNewConnector_AzureClientIDWithoutSecret(t *testing.T) {
	stubAzureCredentials(t, nil)
	c, err := NewConnector(&ConnectionConfig{
		Host: "h", Port: 5432, Database: "d", Username: "u", SSLMode: "require",
		AuthMethod: pgmi.AuthMethodAzureEntraID, AzureClientID: "client-uami",
	}, Output{})
	if err != nil {
		t.Fatal(err)
	}
	tc, ok := c.(*TokenBasedConnector)
	if !ok {
		t.Fatalf("connector is %T", c)
	}
	if got := tc.tokenProvider.String(); got != "AzureManagedIdentity(client=client-uami)" {
		t.Errorf("provider %q, want the managed identity the client ID names", got)
	}
}
