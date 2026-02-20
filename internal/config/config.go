package config

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrConfigNotFound is returned when the config file does not exist.
// Callers can check for this with errors.Is(err, config.ErrConfigNotFound).
var ErrConfigNotFound = errors.New("config file not found")

type ConnectionConfig struct {
	Host               string `yaml:"host"`
	Port               int    `yaml:"port"`
	Username           string `yaml:"username"`
	Database           string `yaml:"database"`
	ManagementDatabase string `yaml:"management_database,omitempty"`
	SSLMode            string `yaml:"sslmode"`
	SSLCert            string `yaml:"sslcert,omitempty"`
	SSLKey             string `yaml:"sslkey,omitempty"`
	SSLRootCert        string `yaml:"sslrootcert,omitempty"`
	AuthMethod         string `yaml:"auth_method,omitempty"`
	AzureTenantID      string `yaml:"azure_tenant_id,omitempty"`
	AzureClientID      string `yaml:"azure_client_id,omitempty"`
	AWSRegion          string `yaml:"aws_region,omitempty"`
	GoogleInstance     string `yaml:"google_instance,omitempty"`
}

type ProjectConfig struct {
	Connection ConnectionConfig  `yaml:"connection"`
	Params     map[string]string `yaml:"params"`
	Timeout    string            `yaml:"timeout"`
}

const ConfigFileName = "pgmi.yaml"

func Load(sourcePath string) (*ProjectConfig, error) {
	configPath := filepath.Join(sourcePath, ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrConfigNotFound
		}
		return nil, err
	}

	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
