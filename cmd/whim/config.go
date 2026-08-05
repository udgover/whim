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
	// ImageEgress records the requested egress mode for custom images. It is
	// separate from Images so older configs remain readable.
	ImageEgress map[string]string `json:"image_egress,omitempty"`
	// ImageEgressConnectors records the exact egress connector ARN used for
	// custom images with connector-backed egress.
	ImageEgressConnectors map[string]string `json:"image_egress_connectors,omitempty"`
	// ImageEgressResourceGroups records the validated no-public-egress topology
	// (VPC, subnet, route table, security group) used by a custom image. For
	// --egress-auto-provision it also records the stable resource-group tag. This is
	// informational, for future tooling such as an explicit cleanup command
	// (see the source plan's Milestone 6): Whim never deletes the AWS
	// resources it names based on this map, only the local cache entry.
	ImageEgressResourceGroups map[string]EgressResourceGroup `json:"image_egress_resource_groups,omitempty"`
}

// EgressResourceGroup is the persisted shape of a validated no-public-egress
// topology. It mirrors microvm.NoPublicEgressResources without
// depending on the microvm package here, keeping config.go's schema stable
// across internal library refactors.
type EgressResourceGroup struct {
	VPCID            string   `json:"vpc_id,omitempty"`
	SubnetIDs        []string `json:"subnet_ids,omitempty"`
	RouteTableID     string   `json:"route_table_id,omitempty"`
	RouteTableIDs    []string `json:"route_table_ids,omitempty"`
	SecurityGroupID  string   `json:"security_group_id,omitempty"`
	SecurityGroupIDs []string `json:"security_group_ids,omitempty"`
	NetworkACLID     string   `json:"network_acl_id,omitempty"`
	ConnectorARN     string   `json:"connector_arn,omitempty"`
	// ResourceGroup is the stable WhimResourceGroup tag value for auto-provisioned
	// resources. It is empty for caller-managed topologies.
	ResourceGroup string `json:"resource_group,omitempty"`
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

// Egress returns the cached egress mode for a custom image name.
func (c *Config) Egress(name string) (string, bool) {
	egress, ok := c.ImageEgress[name]
	return egress, ok
}

// SetEgress records the requested egress mode for a custom image name.
func (c *Config) SetEgress(name, egress string) {
	if c.ImageEgress == nil {
		c.ImageEgress = make(map[string]string)
	}
	c.ImageEgress[name] = egress
}

// EgressConnector returns the cached egress connector ARN for a custom image.
func (c *Config) EgressConnector(name string) (string, bool) {
	connector, ok := c.ImageEgressConnectors[name]
	return connector, ok
}

// SetEgressConnector records the connector ARN used by a custom image.
func (c *Config) SetEgressConnector(name, connectorARN string) {
	if connectorARN == "" {
		if c.ImageEgressConnectors != nil {
			delete(c.ImageEgressConnectors, name)
		}
		return
	}
	if c.ImageEgressConnectors == nil {
		c.ImageEgressConnectors = make(map[string]string)
	}
	c.ImageEgressConnectors[name] = connectorARN
}

// EgressResourceGroup returns the cached no-public-egress resource group for
// a custom image name, and whether one is recorded.
func (c *Config) EgressResourceGroup(name string) (EgressResourceGroup, bool) {
	rg, ok := c.ImageEgressResourceGroups[name]
	return rg, ok
}

// SetEgressResourceGroup records the validated no-public-egress topology used
// by a custom image. Passing the zero value clears any
// existing entry, matching SetEgressConnector's empty-string-clears
// convention. ConnectorARN is required for every managed or caller-supplied
// topology, so its absence is the clear signal.
func (c *Config) SetEgressResourceGroup(name string, rg EgressResourceGroup) {
	if rg.ConnectorARN == "" {
		if c.ImageEgressResourceGroups != nil {
			delete(c.ImageEgressResourceGroups, name)
		}
		return
	}
	if c.ImageEgressResourceGroups == nil {
		c.ImageEgressResourceGroups = make(map[string]EgressResourceGroup)
	}
	c.ImageEgressResourceGroups[name] = rg
}

// removeImageByARN deletes any custom image entries whose cached ARN equals
// arn, returning whether anything was removed. Used to prune the cache after a
// successful `whim image rm` so a deleted image is not left dangling. This
// only ever edits the local config map: it never calls AWS, so it never
// deletes the AWS networking resources an auto-provisioned resource group
// named — that's a deliberately separate, explicit command (Milestone 6).
func (c *Config) removeImageByARN(arn string) bool {
	removed := false
	for name, a := range c.Images {
		if a == arn {
			delete(c.Images, name)
			delete(c.ImageEgress, name)
			delete(c.ImageEgressConnectors, name)
			delete(c.ImageEgressResourceGroups, name)
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
