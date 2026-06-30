package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigPath_ContainsWhim(t *testing.T) {
	p := ConfigPath()
	assert.True(t, strings.Contains(p, "whim"), "config path must be under a 'whim' directory, got: %s", p)
	assert.True(t, strings.HasSuffix(p, "config.json"), "config path must end in config.json")
}

func TestSaveAndLoadConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHIM_CONFIG_DIR", dir)

	cfg := &Config{ImageARN: "arn:aws:lambda:us-east-1:123:microvm-image:test"}
	require.NoError(t, SaveConfig(cfg))

	loaded, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, cfg.ImageARN, loaded.ImageARN)
}

func TestLoadConfig_MissingFile_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHIM_CONFIG_DIR", dir)

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Empty(t, cfg.ImageARN)
}

func TestSaveConfig_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "whim")
	t.Setenv("WHIM_CONFIG_DIR", dir)

	require.NoError(t, SaveConfig(&Config{ImageARN: "arn:x"}))
	_, err := os.Stat(dir)
	assert.NoError(t, err, "SaveConfig must create missing directories")
}

func TestLoadConfig_CorruptJSON_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHIM_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte("not-json{{{"), 0600))

	_, err := LoadConfig()
	require.Error(t, err)
}

func TestLoadConfig_OldFormatImageArnOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHIM_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"image_arn":"arn:aws:lambda:us-east-1:123:microvm-image:whim-default"}`), 0600))

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123:microvm-image:whim-default", cfg.ImageARN)
	assert.Empty(t, cfg.Images, "absent images map loads as empty")
}

func TestSaveConfig_PersistsBothImageArnAndImages(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHIM_CONFIG_DIR", dir)

	cfg := &Config{ImageARN: "arn:default"}
	cfg.SetImage("api", "arn:api")
	require.NoError(t, SaveConfig(cfg))

	loaded, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "arn:default", loaded.ImageARN, "top-level image_arn preserved")
	got, ok := loaded.Image("api")
	assert.True(t, ok)
	assert.Equal(t, "arn:api", got)
}

func TestConfigCustomImage_SetGet(t *testing.T) {
	var cfg Config // zero value: nil Images map
	_, ok := cfg.Image("missing")
	assert.False(t, ok, "unknown image name reports not found")

	cfg.SetImage("api", "arn:api")
	cfg.SetImage("web", "arn:web")
	got, ok := cfg.Image("api")
	assert.True(t, ok)
	assert.Equal(t, "arn:api", got)

	// Overwrite is allowed (name is the cache key).
	cfg.SetImage("api", "arn:api-v2")
	got, _ = cfg.Image("api")
	assert.Equal(t, "arn:api-v2", got)
}

func TestCacheDefaultImage_PreservesCustomImages(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHIM_CONFIG_DIR", dir)
	cfg := &Config{ImageARN: "arn:old-default"}
	cfg.SetImage("api", "arn:api")
	require.NoError(t, SaveConfig(cfg))

	// Re-caching the default (as `whim init` does) must not drop custom images.
	require.NoError(t, cacheDefaultImage("arn:new-default"))

	loaded, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "arn:new-default", loaded.ImageARN, "default ARN updated")
	got, ok := loaded.Image("api")
	assert.True(t, ok, "custom images must survive a default re-cache")
	assert.Equal(t, "arn:api", got)
}

func TestCommandsRegistered(t *testing.T) {
	cmds := rootCmd.Commands()
	names := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		names[c.Name()] = true
	}
	assert.True(t, names["preflight-check"], "preflight-check command must be registered")
	assert.True(t, names["init"], "init command must be registered")
}

func TestInitCmd_HasImageNameAndForceFlags(t *testing.T) {
	assert.NotNil(t, initCmd.Flags().Lookup("image-name"), "init must have --image-name")
	assert.NotNil(t, initCmd.Flags().Lookup("force"), "init must have --force")
	// --image-name defaults to whim-default
	f := initCmd.Flags().Lookup("image-name")
	assert.Equal(t, defaultImageName, f.DefValue)
}

func TestRootCmd_HasRegionAndProfileFlags(t *testing.T) {
	assert.NotNil(t, rootCmd.PersistentFlags().Lookup("region"), "root must have --region")
	assert.NotNil(t, rootCmd.PersistentFlags().Lookup("profile"), "root must have --profile")
}
