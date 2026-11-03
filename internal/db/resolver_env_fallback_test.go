package db

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// libpq fills every parameter a connection string leaves out from the PG*
// environment. pgmi used literals instead, so with PGHOST/PGPORT exported
// "dbname=x" reached localhost:5432 where psql reached the intended server.
func TestResolveConnectionString_FillsOmittedFromEnvironment(t *testing.T) {
	env := &EnvVars{
		PGHOST: "db.example", PGPORT: "6000", PGDATABASE: "maint", PGUSER: "alice",
		PGAPPNAME: "ci-deploy", PGCONNECT_TIMEOUT: "7",
	}
	for _, connStr := range []string{
		"sslmode=disable",
		"postgresql:///?sslmode=disable",
		"SSL Mode=Disable;",
	} {
		t.Run(connStr, func(t *testing.T) {
			cfg, maint, err := ResolveConnectionParams(connStr, nil, nil, nil, nil, nil, env, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := []any{cfg.Host, cfg.Port, cfg.Database, maint, cfg.Username, cfg.AppName, cfg.ConnectTimeout}
			want := []any{"db.example", 6000, "maint", "maint", "alice", "ci-deploy", 7 * time.Second}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("host, port, dbname, maintenance, user, application_name, connect_timeout = %v, want %v", got, want)
					break
				}
			}
		})
	}
}

func TestResolveConnectionString_ExplicitBeatsEnvironment(t *testing.T) {
	env := &EnvVars{PGHOST: "db.example", PGPORT: "6000", PGDATABASE: "maint", PGUSER: "alice", PGAPPNAME: "env-app"}
	cfg, maint, err := ResolveConnectionParams(
		"host=own port=7000 dbname=mine user=bob application_name=own-app", nil, nil, nil, nil, nil, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "own" || cfg.Port != 7000 || maint != "mine" || cfg.Username != "bob" || cfg.AppName != "own-app" {
		t.Errorf("explicit keywords lost to the environment: %+v (maintenance %q)", cfg, maint)
	}
}

func TestResolveConnectionString_DefaultsWithoutEnvironment(t *testing.T) {
	cfg, maint, err := ResolveConnectionParams("sslmode=disable", nil, nil, nil, nil, nil, &EnvVars{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "localhost" || cfg.Port != 5432 || maint != "postgres" || cfg.AppName != "pgmi" {
		t.Errorf("defaults = %s:%d/%s app %q, want localhost:5432/postgres app pgmi", cfg.Host, cfg.Port, maint, cfg.AppName)
	}
}

// pgmi does not read service files; accepting service= or PGSERVICE and then
// connecting elsewhere was the silent wrong-target case. Refuse before dialing.
func TestResolveConnection_RefusesServiceFiles(t *testing.T) {
	cases := []struct {
		conn string
		env  *EnvVars
	}{
		{"service=prod", &EnvVars{}},
		{"postgresql:///?service=prod", &EnvVars{}},
		{"host=db dbname=x", &EnvVars{PGSERVICE: "prod"}},
		{"", &EnvVars{PGSERVICE: "prod"}},
	}
	for _, c := range cases {
		_, _, err := ResolveConnectionParams(c.conn, nil, nil, nil, nil, nil, c.env, nil)
		if !errors.Is(err, pgmi.ErrInvalidConfig) || !strings.Contains(err.Error(), "service") {
			t.Errorf("%q with PGSERVICE=%q: want an invalid-configuration error naming service files, got %v", c.conn, c.env.PGSERVICE, err)
		}
	}
}
