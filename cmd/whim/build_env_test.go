package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultBucketName_Format(t *testing.T) {
	assert.Equal(t, "whim-artifacts-123456789012-us-east-1",
		defaultBucketName("123456789012", "us-east-1"))
}

func TestDefaultBuildRoleName_Stable(t *testing.T) {
	assert.Equal(t, "whim-build-role", defaultBuildRoleName())
}

func TestBuildEnv_HoldsBootstrapFields(t *testing.T) {
	env := buildEnv{
		accountID:    "123456789012",
		region:       "us-east-1",
		bucket:       "whim-artifacts-123456789012-us-east-1",
		buildRoleARN: "arn:aws:iam::123456789012:role/whim-build-role",
		baseImageARN: "arn:aws:lambda:us-east-1:aws:microvm-image:al2023",
	}
	assert.Equal(t, "123456789012", env.accountID)
	assert.Equal(t, "us-east-1", env.region)
	assert.Equal(t, "whim-artifacts-123456789012-us-east-1", env.bucket)
	assert.NotEmpty(t, env.buildRoleARN)
	assert.NotEmpty(t, env.baseImageARN)
}
