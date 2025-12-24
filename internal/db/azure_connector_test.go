package db

import (
	"context"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// MockTokenProvider is a test implementation of TokenProvider.
type MockTokenProvider struct {
	Token     string
	ExpiresOn time.Time
	Err       error
}

func (m *MockTokenProvider) GetToken(ctx context.Context) (string, time.Time, error) {
	if m.Err != nil {
		return "", time.Time{}, m.Err
	}
	return m.Token, m.ExpiresOn, nil
}

func (m *MockTokenProvider) String() string {
	return "MockTokenProvider"
}

func TestAzureEntraIDConnector_Creation(t *testing.T) {
	config := &pgmi.ConnectionConfig{
		Host:       "testserver.postgres.database.azure.com",
		Port:       5432,
		Database:   "testdb",
		Username:   "testuser",
		AuthMethod: pgmi.AuthMethodAzureEntraID,
	}

	mockProvider := &MockTokenProvider{
		Token:     "test-token",
		ExpiresOn: time.Now().Add(1 * time.Hour),
	}

	connector := NewAzureEntraIDConnector(config, mockProvider)

	if connector == nil {
		t.Fatal("expected non-nil connector")
	}

	if connector.config != config {
		t.Error("config not set correctly")
	}

	if connector.tokenProvider != mockProvider {
		t.Error("tokenProvider not set correctly")
	}
}

func TestNewAzureServicePrincipalProvider_RequiresAllParams(t *testing.T) {
	tests := []struct {
		name         string
		tenantID     string
		clientID     string
		clientSecret string
		wantErr      bool
	}{
		{
			name:         "all params provided",
			tenantID:     "tenant-id",
			clientID:     "client-id",
			clientSecret: "client-secret",
			wantErr:      false,
		},
		{
			name:         "missing tenant ID",
			tenantID:     "",
			clientID:     "client-id",
			clientSecret: "client-secret",
			wantErr:      true,
		},
		{
			name:         "missing client ID",
			tenantID:     "tenant-id",
			clientID:     "",
			clientSecret: "client-secret",
			wantErr:      true,
		},
		{
			name:         "missing client secret",
			tenantID:     "tenant-id",
			clientID:     "client-id",
			clientSecret: "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewAzureServicePrincipalProvider(tt.tenantID, tt.clientID, tt.clientSecret)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewAzureServicePrincipalProvider() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewConnector_AzureEntraID(t *testing.T) {
	config := &pgmi.ConnectionConfig{
		Host:              "testserver.postgres.database.azure.com",
		Port:              5432,
		Database:          "testdb",
		Username:          "testuser",
		AuthMethod:        pgmi.AuthMethodAzureEntraID,
		AzureTenantID:     "test-tenant",
		AzureClientID:     "test-client",
		AzureClientSecret: "test-secret",
	}

	connector, err := NewConnector(config)
	if err != nil {
		t.Fatalf("NewConnector() error = %v", err)
	}

	if connector == nil {
		t.Fatal("expected non-nil connector")
	}

	// Verify it's an AzureEntraIDConnector
	_, ok := connector.(*AzureEntraIDConnector)
	if !ok {
		t.Error("expected AzureEntraIDConnector type")
	}
}

func TestApplyAzureAuth(t *testing.T) {
	tests := []struct {
		name               string
		flags              *AzureFlags
		env                *EnvVars
		wantAuthMethod     pgmi.AuthMethod
		wantTenantID       string
		wantClientID       string
		wantClientSecret   string
	}{
		{
			name:           "no Azure config - standard auth",
			flags:          &AzureFlags{},
			env:            &EnvVars{},
			wantAuthMethod: pgmi.AuthMethodStandard,
		},
		{
			name:  "env vars only",
			flags: &AzureFlags{},
			env: &EnvVars{
				AZURE_TENANT_ID:     "env-tenant",
				AZURE_CLIENT_ID:     "env-client",
				AZURE_CLIENT_SECRET: "env-secret",
			},
			wantAuthMethod:   pgmi.AuthMethodAzureEntraID,
			wantTenantID:     "env-tenant",
			wantClientID:     "env-client",
			wantClientSecret: "env-secret",
		},
		{
			name: "flags override env vars",
			flags: &AzureFlags{
				TenantID: "flag-tenant",
				ClientID: "flag-client",
			},
			env: &EnvVars{
				AZURE_TENANT_ID:     "env-tenant",
				AZURE_CLIENT_ID:     "env-client",
				AZURE_CLIENT_SECRET: "env-secret",
			},
			wantAuthMethod:   pgmi.AuthMethodAzureEntraID,
			wantTenantID:     "flag-tenant",
			wantClientID:     "flag-client",
			wantClientSecret: "env-secret", // Secret only from env
		},
		{
			name: "partial flags - tenant only",
			flags: &AzureFlags{
				TenantID: "flag-tenant",
			},
			env: &EnvVars{
				AZURE_CLIENT_ID:     "env-client",
				AZURE_CLIENT_SECRET: "env-secret",
			},
			wantAuthMethod:   pgmi.AuthMethodAzureEntraID,
			wantTenantID:     "flag-tenant",
			wantClientID:     "env-client",
			wantClientSecret: "env-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &pgmi.ConnectionConfig{
				AuthMethod: pgmi.AuthMethodStandard,
			}

			applyAzureAuth(config, tt.flags, tt.env)

			if config.AuthMethod != tt.wantAuthMethod {
				t.Errorf("AuthMethod = %v, want %v", config.AuthMethod, tt.wantAuthMethod)
			}
			if config.AzureTenantID != tt.wantTenantID {
				t.Errorf("AzureTenantID = %v, want %v", config.AzureTenantID, tt.wantTenantID)
			}
			if config.AzureClientID != tt.wantClientID {
				t.Errorf("AzureClientID = %v, want %v", config.AzureClientID, tt.wantClientID)
			}
			if config.AzureClientSecret != tt.wantClientSecret {
				t.Errorf("AzureClientSecret = %v, want %v", config.AzureClientSecret, tt.wantClientSecret)
			}
		})
	}
}
