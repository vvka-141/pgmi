package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_AllFields(t *testing.T) {
	dir := t.TempDir()
	content := `connection:
  host: myhost
  port: 5433
  username: myuser
  database: mydb
  sslmode: require

params:
  env: production
  region: us-west

timeout: 10m
verbose: true
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(content), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "myhost", cfg.Connection.Host)
	assert.Equal(t, 5433, cfg.Connection.Port)
	assert.Equal(t, "myuser", cfg.Connection.Username)
	assert.Equal(t, "mydb", cfg.Connection.Database)
	assert.Equal(t, "require", cfg.Connection.SSLMode)
	assert.Equal(t, "production", cfg.Params["env"])
	assert.Equal(t, "us-west", cfg.Params["region"])
	assert.Equal(t, "10m", cfg.Timeout)
	assert.True(t, cfg.Verbose)
}

func TestLoad_MinimalYAML(t *testing.T) {
	dir := t.TempDir()
	content := `params:
  env: development
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(content), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "", cfg.Connection.Host)
	assert.Equal(t, 0, cfg.Connection.Port)
	assert.Equal(t, "development", cfg.Params["env"])
}

func TestLoad_FileNotFound(t *testing.T) {
	cfg, err := Load(t.TempDir())
	assert.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("{{invalid"), 0644))

	cfg, err := Load(dir)
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestLoad_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(""), 0644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, ProjectConfig{}, *cfg)
}
