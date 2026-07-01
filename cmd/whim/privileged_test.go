package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

// fakeInspector stands in for a microvm.Manager when unit-testing the cached
// privileged-image decision without touching AWS.
type fakeInspector struct {
	caps []microvm.Capability
	err  error
}

func (f fakeInspector) ImageCapabilities(context.Context, string) ([]microvm.Capability, error) {
	return f.caps, f.err
}

func TestPrivilegedImageName(t *testing.T) {
	assert.Equal(t, "whim-privileged", privilegedImageName)
}

// The privileged image must ship the tooling that actually exercises the
// elevated caps — caps unlock syscalls, not binaries.
func TestPrivilegedDockerfile_ShipsTooling(t *testing.T) {
	df := privilegedDockerfileContent
	assert.Contains(t, df, "FROM public.ecr.aws/amazonlinux/amazonlinux:2023")
	for _, pkg := range []string{"util-linux", "iproute", "e2fsprogs", "tar", "gzip"} {
		assert.Containsf(t, df, pkg, "privileged Dockerfile must install %q", pkg)
	}
	assert.Contains(t, df, `CMD ["sleep", "infinity"]`)
}

// A cached ARN is trusted only after verifying it is actually privileged.
func TestCachedPrivilegedUsable_Privileged(t *testing.T) {
	ok, err := cachedPrivilegedUsable(context.Background(),
		fakeInspector{caps: []microvm.Capability{microvm.CapabilityAll}}, "arn:x")
	require.NoError(t, err)
	assert.True(t, ok, "a verified-privileged cached image is reused")
}

func TestCachedPrivilegedUsable_StaleCacheRebuilds(t *testing.T) {
	ok, err := cachedPrivilegedUsable(context.Background(),
		fakeInspector{err: microvm.ErrImageNotFound}, "arn:x")
	require.NoError(t, err)
	assert.False(t, ok, "a deleted cached image is not usable → rebuild")
}

func TestCachedPrivilegedUsable_UnprivilegedRefused(t *testing.T) {
	_, err := cachedPrivilegedUsable(context.Background(),
		fakeInspector{caps: nil}, "arn:x")
	require.Error(t, err, "an existing but unprivileged image must be refused, not silently reused")
}

func TestCachedPrivilegedUsable_LookupErrorPropagates(t *testing.T) {
	_, err := cachedPrivilegedUsable(context.Background(),
		fakeInspector{err: errors.New("boom")}, "arn:x")
	require.Error(t, err)
}
