package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config holds persistent whim state cached on disk.
type Config struct {
	// ImageARN is the ARN of the whim default image built by `whim init`.
	ImageARN string `json:"image_arn,omitempty"`
}

// ConfigPath returns the path to the whim config file.
// The directory can be overridden via WHIM_CONFIG_DIR (used in tests).
func ConfigPath() string {
	dir := os.Getenv("WHIM_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = os.TempDir()
		}
		dir = filepath.Join(base, "whim")
	}
	return filepath.Join(dir, "config.json")
}

// LoadConfig reads the config file. If the file does not exist, an empty
// Config is returned without error. Returns an error only for corrupt JSON
// or unexpected I/O failures.
func LoadConfig() (*Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from os.UserConfigDir(), not user input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveConfig writes cfg to disk, creating missing parent directories.
func SaveConfig(cfg *Config) error {
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
