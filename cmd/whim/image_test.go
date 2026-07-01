package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

func TestClearCacheIfMatches_PreservesCustomImages(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHIM_CONFIG_DIR", dir)
	cfg := &Config{ImageARN: "arn:default"}
	cfg.SetImage("api", "arn:api")
	require.NoError(t, SaveConfig(cfg))

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	clearCacheIfMatches(cmd, "arn:default", false)

	loaded, err := LoadConfig()
	require.NoError(t, err)
	assert.Empty(t, loaded.ImageARN, "the matching default ARN is cleared")
	got, ok := loaded.Image("api")
	assert.True(t, ok, "custom images must survive default-image cache clearing")
	assert.Equal(t, "arn:api", got)
}

func TestClearCacheIfMatches_PrunesDeletedCustomImage(t *testing.T) {
	t.Setenv("WHIM_CONFIG_DIR", t.TempDir())
	cfg := &Config{ImageARN: "arn:default"}
	cfg.SetImage("api", "arn:api")
	cfg.SetImage("web", "arn:web")
	require.NoError(t, SaveConfig(cfg))

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	clearCacheIfMatches(cmd, "arn:api", false)

	loaded, err := LoadConfig()
	require.NoError(t, err)
	_, ok := loaded.Image("api")
	assert.False(t, ok, "the deleted custom image's entry must be pruned from the cache")
	got, ok := loaded.Image("web")
	assert.True(t, ok, "unrelated custom images must survive")
	assert.Equal(t, "arn:web", got)
	assert.Equal(t, "arn:default", loaded.ImageARN, "the default ARN is untouched when a custom image is removed")
}

func TestImageCommandsRegistered(t *testing.T) {
	var imageCmdFound bool
	for _, c := range rootCmd.Commands() {
		if c.Name() != "image" {
			continue
		}
		imageCmdFound = true
		subs := map[string]bool{}
		for _, s := range c.Commands() {
			subs[s.Name()] = true
		}
		assert.True(t, subs["ls"], "image must have an 'ls' subcommand")
		assert.True(t, subs["rm"], "image must have an 'rm' subcommand")
	}
	assert.True(t, imageCmdFound, "image command must be registered on root")
}

func TestImageGroup_HasSharedFlags(t *testing.T) {
	assert.NotNil(t, imageCmd.PersistentFlags().Lookup("quiet"), "image group must have --quiet")
	assert.NotNil(t, imageCmd.PersistentFlags().Lookup("json"), "image group must have --json")
	// inherited by subcommands
	assert.NotNil(t, imageLsCmd.InheritedFlags().Lookup("quiet"))
	assert.NotNil(t, imageRmCmd.InheritedFlags().Lookup("json"))
}

func TestNewOutputMode(t *testing.T) {
	_, err := newOutputMode(true, true)
	require.Error(t, err, "--quiet and --json must be mutually exclusive")

	m, err := newOutputMode(true, false)
	require.NoError(t, err)
	assert.True(t, m.quiet)
	assert.False(t, m.json)

	m, err = newOutputMode(false, true)
	require.NoError(t, err)
	assert.True(t, m.json)

	m, err = newOutputMode(false, false)
	require.NoError(t, err)
	assert.False(t, m.quiet)
	assert.False(t, m.json)
}

func sampleImages() []microvm.ImageSummary {
	return []microvm.ImageSummary{
		{Name: "whim-default", ARN: "arn:a:whim-default", State: "CREATED", Version: "1.0", CreatedAt: time.Date(2026, 6, 26, 16, 19, 0, 0, time.UTC)},
		{Name: "whim-test", ARN: "arn:a:whim-test", State: "CREATING"},
	}
}

func TestRenderImageList_Quiet(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderImageList(&buf, sampleImages(), outputMode{quiet: true}))
	assert.Equal(t, "whim-default\nwhim-test\n", buf.String())
}

func TestRenderImageList_JSON(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderImageList(&buf, sampleImages(), outputMode{json: true}))

	var got []imageJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "whim-default", got[0].Name)
	assert.Equal(t, "CREATED", got[0].State)
	assert.Equal(t, "1.0", got[0].Version)
	assert.Equal(t, "2026-06-26T16:19:00Z", got[0].CreatedAt)
	// whim-test has no createdAt/version → omitted
	assert.Empty(t, got[1].CreatedAt)
	assert.Empty(t, got[1].Version)
}

func TestRenderImageList_JSON_EmptyIsArray(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderImageList(&buf, nil, outputMode{json: true}))
	assert.Equal(t, "[]", strings.TrimSpace(buf.String()))
}

func TestRenderImageList_Table(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderImageList(&buf, sampleImages(), outputMode{}))
	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "whim-default")
	assert.Contains(t, out, "arn:a:whim-test")
}

func TestRenderImageList_Table_EmptyShowsHint(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderImageList(&buf, nil, outputMode{}))
	assert.Contains(t, buf.String(), "No images found")
}
