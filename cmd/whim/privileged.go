package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"

	"github.com/udgover/whim/microvm"
)

// privilegedImageName is the well-known image `whim --privileged` launches. It
// lives in the same Config.Images map as `whim build --name` images, so a user
// can equally build it explicitly with `whim build --privileged --name <this>`.
const privilegedImageName = "whim-privileged"

// privilegedDockerfileContent is the default privileged sandbox image: the same
// AL2023 base as the default image, plus the tooling that actually exercises the
// elevated OS capabilities (util-linux for mount/unshare, iproute for netns,
// e2fsprogs for mkfs). tar/gzip are kept so `whim put`/`get` work. The caps
// themselves are granted via ImageSpec.Capabilities at build time, not here.
const privilegedDockerfileContent = "FROM public.ecr.aws/amazonlinux/amazonlinux:2023\n" +
	"RUN dnf install -y tar gzip util-linux iproute e2fsprogs && dnf clean all\n" +
	"CMD [\"sleep\", \"infinity\"]\n"

// capabilityInspector reads an image's baked OS capabilities. microvm.Manager
// satisfies it; tests supply a fake.
type capabilityInspector interface {
	ImageCapabilities(ctx context.Context, arn string) ([]microvm.Capability, error)
}

// cachedPrivilegedUsable classifies a cached privileged-image ARN:
//   - (true, nil):  the image exists and is privileged — reuse it.
//   - (false, nil): the image is gone (stale cache) — rebuild it.
//   - (false, err): a lookup failed, or the image exists but is NOT privileged
//     (refuse rather than silently launch an unprivileged shell, or delete an
//     image the user may have built deliberately under this name).
func cachedPrivilegedUsable(ctx context.Context, insp capabilityInspector, arn string) (bool, error) {
	caps, err := insp.ImageCapabilities(ctx, arn)
	if errors.Is(err, microvm.ErrImageNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("verify cached %q image: %w", privilegedImageName, err)
	}
	for _, c := range caps {
		if c == microvm.CapabilityAll {
			return true, nil
		}
	}
	return false, fmt.Errorf("cached %q image lacks elevated capabilities; run 'whim image rm %s' and retry",
		privilegedImageName, privilegedImageName)
}

// ensurePrivilegedImage returns the ARN of the cached privileged image, building
// it on first use like `whim init` does for the default image. A cached ARN is
// verified to actually be privileged before reuse. A miss (or stale cache) runs
// the shared bootstrap, uploads the privileged Dockerfile, and builds with
// CapabilityAll — bounded by imageBuildTimeout, with progress on stderr so
// stdout stays clean for `whim run` remote output.
func ensurePrivilegedImage(ctx context.Context, cfg aws.Config, cmd *cobra.Command) (string, error) {
	wcfg, err := LoadConfig()
	if err != nil {
		return "", fmt.Errorf("load whim config: %w", err)
	}
	if arn, ok := wcfg.Image(privilegedImageName); ok && arn != "" {
		usable, verr := cachedPrivilegedUsable(ctx, microvm.NewFromConfig(cfg), arn)
		if verr != nil {
			return "", verr
		}
		if usable {
			return arn, nil
		}
		// Stale cache (image deleted out from under us) — fall through to rebuild.
	}

	// First-use build: bound it independently of the shell/run launch timeout,
	// and route progress to stderr so stdout carries only remote output.
	ctx, cancel := context.WithTimeout(ctx, imageBuildTimeout)
	defer cancel()
	origOut := cmd.OutOrStdout()
	cmd.SetOut(cmd.ErrOrStderr())
	defer cmd.SetOut(origOut)

	printOut(cmd, "No privileged image cached; building %q (this takes ~2-3 minutes)…\n", privilegedImageName)

	env, err := resolveBuildEnv(ctx, cfg, cmd)
	if err != nil {
		return "", err
	}

	artifactKey := privilegedImageName + ".zip"
	if err := uploadPrivilegedDockerfile(ctx, cfg, env.bucket, artifactKey); err != nil {
		return "", fmt.Errorf("upload privileged Dockerfile: %w", err)
	}

	mgr := microvm.NewFromConfig(cfg, microvm.WithAccountID(env.accountID))
	arn, err := mgr.EnsureImage(ctx, microvm.ImageSpec{
		Name:            privilegedImageName,
		BaseImageARN:    env.baseImageARN,
		CodeArtifactURI: fmt.Sprintf("s3://%s/%s", env.bucket, artifactKey),
		BuildRoleARN:    env.buildRoleARN,
		Egress:          microvm.EgressPublic,
		Capabilities:    []microvm.Capability{microvm.CapabilityAll},
	})
	if err != nil {
		return "", fmt.Errorf("build privileged image: %w", err)
	}
	printOut(cmd, "  Privileged image ready: %s\n", arn)

	// Reload before caching so a concurrent build's default/custom ARNs survive.
	wcfg, err = LoadConfig()
	if err != nil {
		return "", fmt.Errorf("reload whim config: %w", err)
	}
	wcfg.SetImage(privilegedImageName, arn)
	if err := SaveConfig(wcfg); err != nil {
		return "", fmt.Errorf("save config: %w", err)
	}
	printOut(cmd, "  Cached %q to %s\n", privilegedImageName, ConfigPath())
	return arn, nil
}

// uploadPrivilegedDockerfile packages the privileged sandbox Dockerfile into a
// ZIP and uploads it to the artifact bucket, mirroring uploadDefaultDockerfile.
func uploadPrivilegedDockerfile(ctx context.Context, cfg aws.Config, bucket, key string) error {
	zipData, err := buildZip("Dockerfile", []byte(privilegedDockerfileContent))
	if err != nil {
		return err
	}
	_, err = s3.NewFromConfig(cfg).PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(zipData),
	})
	return err
}
