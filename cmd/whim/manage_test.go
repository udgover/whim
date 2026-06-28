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

func sampleVMs() []microvm.SandboxInfo {
	return []microvm.SandboxInfo{
		{ID: "microvm-1", ImageARN: "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-default", State: "RUNNING", StartedAt: time.Now().Add(-90 * time.Second)},
		{ID: "microvm-2", ImageARN: "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test", State: "PENDING", StartedAt: time.Now().Add(-5 * time.Minute)},
	}
}

func TestRenderVMList_Table(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderVMList(&buf, sampleVMs(), outputMode{}))
	out := buf.String()
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "microvm-1")
	assert.Contains(t, out, "whim-default", "image name (not full ARN) shown")
	assert.NotContains(t, out, "arn:aws:lambda", "table shows the image name, not the ARN")
	assert.Contains(t, out, "RUNNING")
}

func TestRenderVMList_Quiet(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderVMList(&buf, sampleVMs(), outputMode{quiet: true}))
	assert.Equal(t, "microvm-1\nmicrovm-2\n", buf.String())
}

func TestRenderVMList_JSON(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderVMList(&buf, sampleVMs(), outputMode{json: true}))
	var arr []vmJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &arr))
	require.Len(t, arr, 2)
	assert.Equal(t, "microvm-1", arr[0].ID)
	assert.Equal(t, "whim-default", arr[0].Image)
	assert.NotEmpty(t, arr[0].StartedAt)
}

func TestRenderVMList_Empty(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderVMList(&buf, nil, outputMode{}))
	assert.Contains(t, buf.String(), "No whim MicroVMs")
}

func TestFilterOlderThan(t *testing.T) {
	vms := sampleVMs() // ages ~90s and ~5m
	assert.Len(t, filterOlderThan(vms, 0), 2, "0 = all")
	assert.Len(t, filterOlderThan(vms, 2*time.Minute), 1, "only the ~5m-old one")
	assert.Len(t, filterOlderThan(vms, time.Hour), 0)
}

func TestConfirm(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "Y\n": true, "n\n": false, "\n": false, "nope\n": false} {
		cmd := &cobra.Command{}
		cmd.SetIn(strings.NewReader(in))
		cmd.SetErr(&bytes.Buffer{})
		assert.Equalf(t, want, confirm(cmd, "ok?"), "input %q", in)
	}
}

func TestPsGcCommandsRegistered(t *testing.T) {
	cmds := map[string]*cobra.Command{}
	for _, c := range rootCmd.Commands() {
		cmds[c.Name()] = c
	}
	require.Contains(t, cmds, "ps")
	require.Contains(t, cmds, "gc")
	assert.NotNil(t, cmds["ps"].RunE)
	assert.NotNil(t, cmds["gc"].RunE)
	require.NotContains(t, cmds, "ls", "the VM list command is `ps`, not `ls` (image ls is separate)")
}
