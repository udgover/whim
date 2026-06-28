package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSuspendDoneMsg(t *testing.T) {
	assert.Equal(t, "suspended microvm-x", suspendDoneMsg("suspend", "microvm-x"))
	assert.Equal(t, "resumed microvm-x", suspendDoneMsg("resume", "microvm-x"))
	assert.NotContains(t, suspendDoneMsg("suspend", "id"), "suspendd", "no double-d grammar bug")
}

func TestSuspendResumeCommandsRegistered(t *testing.T) {
	cmds := map[string]*cobra.Command{}
	for _, c := range rootCmd.Commands() {
		cmds[c.Name()] = c
	}
	for _, name := range []string{"suspend", "resume"} {
		c, ok := cmds[name]
		require.True(t, ok, "%s must be registered", name)
		assert.NotNil(t, c.RunE)
		require.Error(t, c.Args(c, []string{}), "%s requires a microvm id", name)
		assert.NoError(t, c.Args(c, []string{"microvm-x"}))
	}
}
