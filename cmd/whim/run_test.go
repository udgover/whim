package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExitCodeError_CarriesCode(t *testing.T) {
	err := error(exitCodeError{7})
	var ece exitCodeError
	require.True(t, errors.As(err, &ece))
	assert.Equal(t, 7, ece.code)
	assert.Contains(t, err.Error(), "7")
}

func TestWhimFail_ReturnsReservedCode(t *testing.T) {
	cmd := &cobra.Command{}
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	err := whimFail(cmd, "launch", errors.New("boom"))

	var ece exitCodeError
	require.True(t, errors.As(err, &ece))
	assert.GreaterOrEqual(t, ece.code, 125, "whim failures must not collide with remote 0-124 exit codes")
	assert.Contains(t, errBuf.String(), "boom", "the underlying error is shown to the user")
}

func TestRunAndExecCommandsRegistered(t *testing.T) {
	cmds := map[string]*cobra.Command{}
	for _, c := range rootCmd.Commands() {
		cmds[c.Name()] = c
	}
	require.Contains(t, cmds, "run")
	require.Contains(t, cmds, "exec")
	assert.NotNil(t, cmds["run"].RunE)
	assert.NotNil(t, cmds["exec"].RunE)
}
