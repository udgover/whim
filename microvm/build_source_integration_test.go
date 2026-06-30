//go:build integration

// Real-AWS, opt-in integration coverage for BuildFromSource.
// Run with: WHIM_INTEGRATION=1 go test -tags=integration ./microvm/ -v
//
// The local-directory and egress-none builds run with just WHIM_INTEGRATION=1,
// assuming `whim init` has provisioned the default artifact bucket and build
// role (override via WHIM_TEST_ARTIFACT_BUCKET / WHIM_TEST_BUILD_ROLE /
// WHIM_TEST_BASE_IMAGE). The HTTPS-zip build is gated on WHIM_TEST_HTTPS_ZIP.
package microvm_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

// buildInputs holds the explicit inputs BuildFromSource requires, derived from
// the ambient account/region to match `whim init` defaults.
type buildInputs struct {
	mgr    *microvm.Manager
	bucket string
	base   string
	role   string
}

// integrationBuildInputs resolves a Manager and the build inputs from the
// ambient AWS config. It skips the test unless WHIM_INTEGRATION=1.
func integrationBuildInputs(t *testing.T, ctx context.Context) buildInputs {
	t.Helper()
	if os.Getenv("WHIM_INTEGRATION") != "1" {
		t.Skip("set WHIM_INTEGRATION=1 to run real-AWS integration tests")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, cfg.Region)
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	require.NoError(t, err)
	account := aws.ToString(ident.Account)

	mgr := microvm.NewFromConfig(cfg,
		microvm.WithAccountID(account),
		microvm.WithPollInterval(2*time.Second),
	)

	bucket := os.Getenv("WHIM_TEST_ARTIFACT_BUCKET")
	if bucket == "" {
		bucket = fmt.Sprintf("whim-artifacts-%s-%s", account, cfg.Region)
	}
	role := os.Getenv("WHIM_TEST_BUILD_ROLE")
	if role == "" {
		role = fmt.Sprintf("arn:aws:iam::%s:role/whim-build-role", account)
	}
	base := os.Getenv("WHIM_TEST_BASE_IMAGE")
	if base == "" {
		out, err := lambdamicrovms.NewFromConfig(cfg).ListManagedMicrovmImages(ctx, nil)
		require.NoError(t, err)
		require.NotEmpty(t, out.Items, "no managed base images in this region")
		arns := make([]string, 0, len(out.Items))
		for _, it := range out.Items {
			arns = append(arns, aws.ToString(it.ImageArn))
		}
		sort.Strings(arns)
		base = arns[0]
		for _, a := range arns {
			if strings.Contains(a, "al2023") {
				base = a
				break
			}
		}
	}
	return buildInputs{mgr: mgr, bucket: bucket, base: base, role: role}
}

// localBuildContext writes a minimal but real build context to a temp dir.
func localBuildContext(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const dockerfile = "FROM public.ecr.aws/amazonlinux/amazonlinux:2023\n" +
		"RUN dnf install -y tar gzip && dnf clean all\n" +
		"CMD [\"sleep\", \"infinity\"]\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))
	return dir
}

// buildAndCleanup force-builds source under name and schedules image deletion.
func buildAndCleanup(t *testing.T, ctx context.Context, in buildInputs, name, source string, egress microvm.EgressMode) string {
	t.Helper()
	arn, err := in.mgr.BuildFromSource(ctx, source, microvm.BuildFromSourceOptions{
		Name:           name,
		ArtifactBucket: in.bucket,
		BaseImageARN:   in.base,
		BuildRoleARN:   in.role,
		Egress:         egress,
		Force:          true, // a clean build each run; cleanup removes it after
	})
	require.NoError(t, err)
	require.Contains(t, arn, ":microvm-image:"+name)
	t.Cleanup(func() { _ = in.mgr.DeleteImage(context.Background(), arn) })
	return arn
}

func TestIntegration_BuildFromLocalDirectory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	in := integrationBuildInputs(t, ctx)

	arn := buildAndCleanup(t, ctx, in, "whim-it-localdir", localBuildContext(t), microvm.EgressPublic)
	t.Logf("built %s", arn)

	// Prove the built image actually launches and runs.
	sb, err := in.mgr.Launch(ctx, arn, microvm.WithTTL(5*time.Minute))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	res, err := sb.Exec(ctx, []string{"sh", "-c", "echo $((6*7))"})
	require.NoError(t, err)
	assert.Equal(t, "42", strings.TrimSpace(string(res.Output)), "built image must run commands")
}

func TestIntegration_BuildEgressNone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	in := integrationBuildInputs(t, ctx)

	// Covers --egress none: the image must build and launch. Verifying the
	// absence of outbound internet end-to-end is a manual check (guest tooling
	// varies); building/launching an airgapped image is the automated signal.
	arn := buildAndCleanup(t, ctx, in, "whim-it-egress-none", localBuildContext(t), microvm.EgressNone)
	sb, err := in.mgr.Launch(ctx, arn, microvm.WithTTL(5*time.Minute))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("egress-none image launched: id=%s", sb.ID())
}

func TestIntegration_BuildFromHTTPSZip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	in := integrationBuildInputs(t, ctx)

	url := os.Getenv("WHIM_TEST_HTTPS_ZIP")
	if url == "" {
		t.Skip("set WHIM_TEST_HTTPS_ZIP to an https:// zip archive (Dockerfile at root) to run this")
	}
	arn := buildAndCleanup(t, ctx, in, "whim-it-https-zip", url, microvm.EgressPublic)
	t.Logf("built from HTTPS zip: %s", arn)
}

func TestIntegration_PrivateECRBaseImage_Manual(t *testing.T) {
	t.Skip("private-ECR base images are out of v0.1: the build-role IAM is not " +
		"widened until that path is validated — see README. Validate manually.")
}
