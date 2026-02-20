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

func TestTokenBasedConnector_Creation(t *testing.T) {
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

	connector := NewTokenBasedConnector(config, mockProvider, "Azure")

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

	_, ok := connector.(*TokenBasedConnector)
	if !ok {
		t.Error("expected TokenBasedConnector type")
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
			name:  "env vars without --azure stays Standard",
			flags: &AzureFlags{},
			env: &EnvVars{
				AZURE_TENANT_ID:     "env-tenant",
				AZURE_CLIENT_ID:     "env-client",
				AZURE_CLIENT_SECRET: "env-secret",
			},
			wantAuthMethod: pgmi.AuthMethodStandard,
		},
		{
			name: "--azure with flags override env vars",
			flags: &AzureFlags{
				Enabled:  true,
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
			wantClientSecret: "env-secret",
		},
		{
			name: "--azure with partial flags",
			flags: &AzureFlags{
				Enabled:  true,
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
		{
			name:           "--azure alone activates AzureEntraID",
			flags:          &AzureFlags{Enabled: true},
			env:            &EnvVars{},
			wantAuthMethod: pgmi.AuthMethodAzureEntraID,
		},
		{
			name:  "--azure with env vars",
			flags: &AzureFlags{Enabled: true},
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
			name: "--azure with explicit flags",
			flags: &AzureFlags{
				Enabled:  true,
				TenantID: "flag-tenant",
				ClientID: "flag-client",
			},
			env: &EnvVars{
				AZURE_CLIENT_SECRET: "env-secret",
			},
			wantAuthMethod:   pgmi.AuthMethodAzureEntraID,
			wantTenantID:     "flag-tenant",
			wantClientID:     "flag-client",
			wantClientSecret: "env-secret",
		},
		{
			name:           "--azure false with no creds stays Standard",
			flags:          &AzureFlags{},
			env:            &EnvVars{},
			wantAuthMethod: pgmi.AuthMethodStandard,
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

func TestAzureFlags_IsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		flags *AzureFlags
		want  bool
	}{
		{"nil", nil, true},
		{"empty struct", &AzureFlags{}, true},
		{"Enabled only", &AzureFlags{Enabled: true}, false},
		{"TenantID only", &AzureFlags{TenantID: "t"}, false},
		{"ClientID only", &AzureFlags{ClientID: "c"}, false},
		{"Enabled with IDs", &AzureFlags{Enabled: true, TenantID: "t", ClientID: "c"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.flags.IsEmpty(); got != tt.want {
				t.Errorf("AzureFlags.IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}
