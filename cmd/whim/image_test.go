package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

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
