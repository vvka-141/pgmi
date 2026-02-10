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
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Username    string `yaml:"username"`
	Database    string `yaml:"database"`
	SSLMode     string `yaml:"sslmode"`
	SSLCert     string `yaml:"sslcert"`
	SSLKey      string `yaml:"sslkey"`
	SSLRootCert string `yaml:"sslrootcert"`
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
