package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type sslModeSource struct {
	name      string
	connStr   string
	flags     *GranularConnFlags
	env       *EnvVars
	pc        *config.ProjectConfig
	wantLabel string
}

func sslModeSources(mode string) []sslModeSource {
	return []sslModeSource{
		{name: "flag", flags: &GranularConnFlags{Host: "db.example.com", SSLMode: mode}, wantLabel: "--sslmode"},
		{name: "PGSSLMODE with granular", flags: &GranularConnFlags{Host: "db.example.com"}, env: &EnvVars{PGSSLMODE: mode}, wantLabel: "$PGSSLMODE"},
		{name: "PGSSLMODE with connection string", connStr: "postgresql://u@db.example.com/postgres", env: &EnvVars{PGSSLMODE: mode}, wantLabel: "$PGSSLMODE"},
		{name: "connection string", connStr: "postgresql://u@db.example.com/postgres?sslmode=" + mode, wantLabel: "--connection"},
		{name: "PGMI_CONNECTION_STRING", env: &EnvVars{PGMI_CONNECTION_STRING: "postgresql://u@db.example.com/postgres?sslmode=" + mode}, wantLabel: "PGMI_CONNECTION_STRING"},
		{name: "pgmi.yaml", flags: &GranularConnFlags{}, env: &EnvVars{PGHOST: "db.example.com"}, pc: &config.ProjectConfig{Connection: config.ConnectionConfig{SSLMode: mode}}, wantLabel: "pgmi.yaml connection.sslmode"},
	}
}

type tokenProvider struct {
	name   string
	azure  *AzureFlags
	aws    *AWSFlags
	method pgmi.AuthMethod
}

var tokenProviders = []tokenProvider{
	{name: "azure", azure: &AzureFlags{Enabled: true}, method: pgmi.AuthMethodAzureEntraID},
	{name: "aws", aws: &AWSFlags{Enabled: true, Region: "us-east-1"}, method: pgmi.AuthMethodAWSIAM},
}

func resolveWith(src sslModeSource, p tokenProvider, certs *CertFlags) (*ConnectionConfig, error) {
	cfg, _, err := ResolveConnectionParams(src.connStr, src.flags, p.azure, p.aws, nil, certs, src.env, src.pc)
	return cfg, err
}

func TestResolveConnectionParams_TokenAuthRefusesPlaintextCapableSSLMode(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer"} {
		for _, src := range sslModeSources(mode) {
			for _, p := range tokenProviders {
				t.Run(p.name+"/"+src.name+"/"+mode, func(t *testing.T) {
					_, err := resolveWith(src, p, nil)
					if !errors.Is(err, pgmi.ErrInvalidConfig) {
						t.Fatalf("want ErrInvalidConfig, got %v", err)
					}
					for _, want := range []string{`"` + mode + `"`, src.wantLabel, p.method.String()} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("error %q does not name %q", err, want)
						}
					}
				})
			}
		}
	}
}

func TestResolveConnectionParams_TokenAuthAcceptsTLSOnlySSLMode(t *testing.T) {
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		for _, src := range sslModeSources(mode) {
			for _, p := range tokenProviders {
				t.Run(p.name+"/"+src.name+"/"+mode, func(t *testing.T) {
					cfg, err := resolveWith(src, p, nil)
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if cfg.SSLMode != mode || cfg.AuthMethod != p.method {
						t.Errorf("got sslmode %q auth %v, want %q %v", cfg.SSLMode, cfg.AuthMethod, mode, p.method)
					}
				})
			}
		}
	}
}

func TestResolveConnectionParams_TokenAuthDefaultSSLMode(t *testing.T) {
	unset := []sslModeSource{
		{name: "granular", flags: &GranularConnFlags{Host: "db.example.com"}},
		{name: "connection string", connStr: "postgresql://u@db.example.com/postgres"},
	}
	tests := []struct {
		provider tokenProvider
		certs    *CertFlags
		want     string
	}{
		{provider: tokenProviders[0], want: "verify-full"},
		{provider: tokenProviders[1], want: "require"},
		{provider: tokenProviders[1], certs: &CertFlags{SSLRootCert: "/etc/ssl/rds-global-bundle.pem"}, want: "verify-full"},
	}
	for _, tt := range tests {
		for _, src := range unset {
			t.Run(tt.provider.name+"/"+src.name+"/"+tt.want, func(t *testing.T) {
				cfg, err := resolveWith(src, tt.provider, tt.certs)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.SSLMode != tt.want {
					t.Errorf("sslmode = %q, want %q", cfg.SSLMode, tt.want)
				}
			})
		}
	}
}

func TestResolveConnectionParams_NonTokenAuthKeepsPreferDefaultAndWeakModes(t *testing.T) {
	google := &GoogleFlags{Enabled: true, Instance: "p:r:i"}
	for _, tt := range []struct {
		name   string
		google *GoogleFlags
		flags  *GranularConnFlags
		want   string
	}{
		{name: "standard default", flags: &GranularConnFlags{Host: "localhost"}, want: "prefer"},
		{name: "standard disable", flags: &GranularConnFlags{Host: "localhost", SSLMode: "disable"}, want: "disable"},
		{name: "google default", google: google, flags: &GranularConnFlags{}, want: "prefer"},
		{name: "google disable", google: google, flags: &GranularConnFlags{SSLMode: "disable"}, want: "disable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _, err := ResolveConnectionParams("", tt.flags, nil, nil, tt.google, nil, &EnvVars{}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.SSLMode != tt.want {
				t.Errorf("sslmode = %q, want %q", cfg.SSLMode, tt.want)
			}
		})
	}
}
