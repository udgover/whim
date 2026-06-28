package main

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildZip_ProducesReadableArchive(t *testing.T) {
	content := []byte("FROM scratch\nCMD [\"true\"]\n")
	data, err := buildZip("Dockerfile", content)
	require.NoError(t, err)

	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	require.Len(t, r.File, 1)
	assert.Equal(t, "Dockerfile", r.File[0].Name)

	f, err := r.File[0].Open()
	require.NoError(t, err)
	defer f.Close()
	got, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestDefaultBucketName_Format(t *testing.T) {
	assert.Equal(t, "whim-artifacts-123456789012-us-east-1",
		defaultBucketName("123456789012", "us-east-1"))
}

func TestDefaultBuildRoleName_Stable(t *testing.T) {
	assert.Equal(t, "whim-build-role", defaultBuildRoleName())
}
