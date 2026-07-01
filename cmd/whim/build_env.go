package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"
)

// buildEnv holds the shared bootstrap resources that both `whim init` and
// `whim build` need: caller identity (account/region) plus the artifact bucket,
// build role, and managed base image. It is the seam that lets build reuse
// init's one-time provisioning without duplicating it.
type buildEnv struct {
	accountID    string
	region       string
	bucket       string
	buildRoleARN string
	baseImageARN string
}

// resolveBuildEnv performs the bootstrap shared by init and build: resolve the
// caller's account/region, ensure the artifact bucket and build role exist, and
// select the managed base image. It emits the same progress lines `whim init`
// has always printed, in the same order, so init output is unchanged.
func resolveBuildEnv(ctx context.Context, cfg aws.Config, cmd *cobra.Command) (buildEnv, error) {
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	if err != nil {
		return buildEnv{}, fmt.Errorf("resolve account ID: %w", err)
	}
	env := buildEnv{accountID: aws.ToString(out.Account), region: cfg.Region}

	printOut(cmd, "Initialising whim in account %s / region %s\n", env.accountID, env.region)

	// Step 1: S3 bucket.
	env.bucket = defaultBucketName(env.accountID, env.region)
	if err := ensureBucket(ctx, cfg, env.bucket, env.region, cmd); err != nil {
		return buildEnv{}, err
	}

	// Step 2: IAM build role.
	roleARN, err := ensureBuildRole(ctx, cfg, defaultBuildRoleName(), env.bucket, cmd)
	if err != nil {
		return buildEnv{}, err
	}
	env.buildRoleARN = roleARN

	// Step 3: Managed base image ARN.
	baseARN, err := managedBaseImageARN(ctx, cfg)
	if err != nil {
		return buildEnv{}, fmt.Errorf("list managed images: %w", err)
	}
	env.baseImageARN = baseARN
	printOut(cmd, "  Using base image: %s\n", baseARN)

	return env, nil
}

// defaultBucketName returns the deterministic S3 bucket name for whim artifacts.
func defaultBucketName(accountID, region string) string {
	return fmt.Sprintf("whim-artifacts-%s-%s", accountID, region)
}

// defaultBuildRoleName returns the IAM build role name (account-scoped, fixed).
func defaultBuildRoleName() string {
	return "whim-build-role"
}
