package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

func TestBuildCmd_Registered(t *testing.T) {
	var found *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "build" {
			found = c
		}
	}
	require.NotNil(t, found, "build command must be registered")
	assert.NotNil(t, found.RunE)
}

func TestBuildCmd_RequiresExactlyOneSource(t *testing.T) {
	require.Error(t, buildCmd.Args(buildCmd, []string{}), "zero args rejected")
	require.Error(t, buildCmd.Args(buildCmd, []string{"a", "b"}), "two args rejected")
	require.NoError(t, buildCmd.Args(buildCmd, []string{"./src"}), "exactly one source accepted")
}

func TestBuildFlags_Present(t *testing.T) {
	for _, name := range []string{"name", "egress", "force", "context-subdir", "json"} {
		assert.NotNilf(t, buildCmd.Flags().Lookup(name), "build must define --%s", name)
	}
	assert.Equal(t, "public", buildCmd.Flags().Lookup("egress").DefValue, "egress defaults to public")
}

func TestBuildFlags_NameRequired(t *testing.T) {
	f := buildCmd.Flags().Lookup("name")
	require.NotNil(t, f)
	assert.Contains(t, f.Annotations, cobra.BashCompOneRequiredFlag,
		"--name must be marked required so a missing name fails before any AWS call")
}

func TestBuildEgress_Mapping(t *testing.T) {
	got, err := parseEgress("public")
	require.NoError(t, err)
	assert.Equal(t, microvm.EgressPublic, got)

	got, err = parseEgress("none")
	require.NoError(t, err)
	assert.Equal(t, microvm.EgressNone, got)

	_, err = parseEgress("vpc")
	require.Error(t, err, "unsupported egress must fail (before any AWS call)")
	_, err = parseEgress("")
	require.Error(t, err)
}
