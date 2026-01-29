package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ConnectionConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Database string `yaml:"database"`
	SSLMode  string `yaml:"sslmode"`
}

type ProjectConfig struct {
	Connection ConnectionConfig  `yaml:"connection"`
	Params     map[string]string `yaml:"params"`
	Timeout    string            `yaml:"timeout"`
	Verbose    bool              `yaml:"verbose"`
}

const ConfigFileName = "pgmi.yaml"

func Load(sourcePath string) (*ProjectConfig, error) {
	configPath := filepath.Join(sourcePath, ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
