package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionString(t *testing.T) {
	s := versionString()
	assert.True(t, strings.HasPrefix(s, "whim "), "version string starts with the binary name, got %q", s)
	assert.NotEqual(t, "whim ", s, "must include a version token")
}

func TestVersionCommandRegistered(t *testing.T) {
	var found *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "version" {
			found = c
		}
	}
	require.NotNil(t, found)
	assert.NotNil(t, found.RunE)
}
