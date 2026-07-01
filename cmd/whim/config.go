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
	// Images maps a custom image name (as passed to `whim build --name`) to its
	// built ARN. It is additive: older configs with only image_arn load fine,
	// and the default image continues to live in ImageARN.
	Images map[string]string `json:"images,omitempty"`
}

// Image returns the cached ARN for a custom image name, and whether it exists.
func (c *Config) Image(name string) (string, bool) {
	arn, ok := c.Images[name]
	return arn, ok
}

// SetImage records the built ARN for a custom image name, creating the map on
// first use. A repeated name overwrites, since the name is the build cache key.
func (c *Config) SetImage(name, arn string) {
	if c.Images == nil {
		c.Images = make(map[string]string)
	}
	c.Images[name] = arn
}

// removeImageByARN deletes any custom image entries whose cached ARN equals
// arn, returning whether anything was removed. Used to prune the cache after a
// successful `whim image rm` so a deleted image is not left dangling.
func (c *Config) removeImageByARN(arn string) bool {
	removed := false
	for name, a := range c.Images {
		if a == arn {
			delete(c.Images, name)
			removed = true
		}
	}
	return removed
}

// cacheDefaultImage updates only the default image ARN in the persisted config,
// preserving any custom Images map. Use this instead of writing a fresh Config,
// which would silently drop images cached by `whim build --name`.
func cacheDefaultImage(arn string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	cfg.ImageARN = arn
	return SaveConfig(cfg)
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
