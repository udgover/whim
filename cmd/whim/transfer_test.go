package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutGetCommandsRegistered(t *testing.T) {
	cmds := map[string]*cobra.Command{}
	for _, c := range rootCmd.Commands() {
		cmds[c.Name()] = c
	}
	for _, name := range []string{"put", "get"} {
		c, ok := cmds[name]
		require.True(t, ok, "%s must be registered", name)
		assert.NotNil(t, c.RunE)
		// <id> <a> <b> — exactly 3 positional args.
		assert.Error(t, c.Args(c, []string{"only-one"}), "%s requires 3 args", name)
		assert.NoError(t, c.Args(c, []string{"id", "a", "b"}))
	}
}
