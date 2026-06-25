package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
	Email     string `json:"email,omitempty"` // who the saved token belongs to (for whoami)
	// Default cloud account used by `resources create` when --account is omitted
	// (set via `autodev cloud use`). ID is authoritative; Name is for display.
	DefaultAccount     string `json:"default_account,omitempty"`
	DefaultAccountName string `json:"default_account_name,omitempty"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".autodev", "config.json")
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{ServerURL: "http://localhost:8080"}, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveConfig(c *Config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}
