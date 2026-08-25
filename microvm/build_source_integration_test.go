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
	"errors"
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
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

// buildInputs holds the explicit inputs BuildFromSource requires, derived from
// the ambient account/region to match `whim init` defaults.
type buildInputs struct {
	mgr      *microvm.Manager
	microvms *lambdamicrovms.Client
	bucket   string
	base     string
	role     string
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
	return buildInputs{mgr: mgr, microvms: lambdamicrovms.NewFromConfig(cfg), bucket: bucket, base: base, role: role}
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
func buildAndCleanup(t *testing.T, ctx context.Context, in buildInputs, name, source string, egress microvm.EgressMode, connectorARN string) string {
	t.Helper()
	arn, err := in.mgr.BuildFromSource(ctx, source, microvm.BuildFromSourceOptions{
		Name:               name,
		ArtifactBucket:     in.bucket,
		BaseImageARN:       in.base,
		BuildRoleARN:       in.role,
		Egress:             egress,
		EgressConnectorARN: connectorARN,
		Force:              true, // a clean build each run; cleanup removes it after
	})
	require.NoError(t, err)
	require.Contains(t, arn, ":microvm-image:"+name)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := in.mgr.DeleteImage(cleanupCtx, arn); err != nil {
			t.Errorf("cleanup image %s: %v", arn, err)
		}
	})
	return arn
}

// cleanupSandbox registers after buildAndCleanup, so t.Cleanup's LIFO order
// waits for the VM to reach TERMINATED before image deletion is attempted.
func cleanupSandbox(t *testing.T, in buildInputs, sb *microvm.Sandbox) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := sb.Terminate(ctx); err != nil {
			t.Errorf("cleanup microvm %s: terminate: %v", sb.ID(), err)
			return
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			out, err := in.microvms.GetMicrovm(ctx, &lambdamicrovms.GetMicrovmInput{MicrovmIdentifier: aws.String(sb.ID())})
			var notFound *lambdatypes.ResourceNotFoundException
			if errors.As(err, &notFound) {
				return
			}
			if err != nil {
				t.Errorf("cleanup microvm %s: wait for termination: %v", sb.ID(), err)
				return
			}
			if out.State == lambdatypes.MicrovmStateTerminated {
				return
			}
			select {
			case <-ctx.Done():
				t.Errorf("cleanup microvm %s: wait for termination: %v", sb.ID(), ctx.Err())
				return
			case <-ticker.C:
			}
		}
	})
}

func TestIntegration_BuildFromLocalDirectory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	in := integrationBuildInputs(t, ctx)

	arn := buildAndCleanup(t, ctx, in, "whim-it-localdir", localBuildContext(t), microvm.EgressPublic, "")
	t.Logf("built %s", arn)

	// Prove the built image actually launches and runs.
	sb, err := in.mgr.Launch(ctx, arn, microvm.WithTTL(5*time.Minute))
	require.NoError(t, err)
	cleanupSandbox(t, in, sb)
	res, err := sb.Exec(ctx, []string{"sh", "-c", "echo $((6*7))"})
	require.NoError(t, err)
	assert.Equal(t, "42", strings.TrimSpace(string(res.Output)), "built image must run commands")
}

func TestIntegration_PublicBuildWithNoPublicRuntimeEgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	in := integrationBuildInputs(t, ctx)

	connectorARN := os.Getenv("WHIM_TEST_EGRESS_CONNECTOR")
	if connectorARN == "" {
		t.Skip("set WHIM_TEST_EGRESS_CONNECTOR to an isolated Lambda Core VPC connector ARN")
	}
	validated, err := in.mgr.ValidateNoPublicEgressConnector(ctx, microvm.NoPublicEgressResources{ConnectorARN: connectorARN})
	require.NoError(t, err, "the supplied connector must prove the no-public-egress contract")
	// Image creation needs public egress; the isolated connector is an explicit,
	// topology-validated runtime override.
	arn := buildAndCleanup(t, ctx, in, "whim-it-egress-none", localBuildContext(t), microvm.EgressPublic, "")
	sb, err := in.mgr.Launch(ctx, arn,
		microvm.WithTTL(5*time.Minute),
		microvm.WithEgressConnector(microvm.EgressNone, connectorARN),
		microvm.WithExpectedNoPublicEgress(*validated))
	require.NoError(t, err)
	cleanupSandbox(t, in, sb)
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
	arn := buildAndCleanup(t, ctx, in, "whim-it-https-zip", url, microvm.EgressPublic, "")
	t.Logf("built from HTTPS zip: %s", arn)
}

func TestIntegration_PrivateECRBaseImage_Manual(t *testing.T) {
	t.Skip("private-ECR base images are out of v0.1: the build-role IAM is not " +
		"widened until that path is validated — see README. Validate manually.")
}

// probeBuildContext writes a build context for guest egress probes. Amazon
// Linux 2023's base image already ships curl-minimal (sufficient for our
// probes: -s/--connect-timeout both work) and glibc's getent, so no RUN
// step — and so no build-time network access — is needed at all. An
// earlier version of this helper ran `dnf install -y curl`, which fails:
// it conflicts with the preinstalled curl-minimal package.
func probeBuildContext(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const dockerfile = "FROM public.ecr.aws/amazonlinux/amazonlinux:2023\n" +
		"CMD [\"sleep\", \"infinity\"]\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))
	return dir
}

// TestIntegration_NoPublicEgress provisions (or reuses) a Whim-managed
// no-public-egress resource group via EnsureNoPublicEgressConnector, builds
// the minimal probe image with public egress, then launches it with the exact
// isolated connector as a validated runtime override. It proves the MVP
// NO_PUBLIC_EGRESS security contract: MicroVM metadata never reports
// INTERNET_EGRESS, and direct public IP / HTTPS hostname connections from
// the guest fail. DNS behavior is recorded, not asserted — MVP does not
// claim DNS resolution is blocked; strict DNS is tracked separately in #7.
func TestIntegration_NoPublicEgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	in := integrationBuildInputs(t, ctx)

	operatorRole := os.Getenv("WHIM_TEST_EGRESS_OPERATOR_ROLE")
	if operatorRole == "" {
		t.Skip("set WHIM_TEST_EGRESS_OPERATOR_ROLE to an IAM role ARN trusting lambda.amazonaws.com " +
			"with AWSLambdaNetworkConnectorOperatorPolicy attached (Whim does not create this role itself)")
	}

	resources, err := in.mgr.EnsureNoPublicEgressConnector(ctx, microvm.NoPublicEgressSpec{
		NamePrefix:      "whim-it",
		OperatorRoleARN: operatorRole,
		VPCCIDRBlock:    "10.243.99.0/24",
		SubnetCIDRBlock: "10.243.99.0/25",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resources.ConnectorARN)
	require.NotContains(t, resources.ConnectorARN, "INTERNET_EGRESS")
	t.Logf("no-public-egress resource group: vpc=%s subnet=%v rt=%s sg=%s connector=%s",
		resources.VPCID, resources.SubnetIDs, resources.RouteTableID, resources.SecurityGroupID, resources.ConnectorARN)

	arn := buildAndCleanup(t, ctx, in, "whim-it-no-public-egress-probe", probeBuildContext(t), microvm.EgressPublic, "")

	sb, err := in.mgr.Launch(ctx, arn, microvm.WithTTL(5*time.Minute),
		microvm.WithEgressConnector(microvm.EgressNone, resources.ConnectorARN),
		microvm.WithExpectedNoPublicEgress(*resources))
	require.NoError(t, err)
	cleanupSandbox(t, in, sb)

	// Metadata must never report INTERNET_EGRESS.
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	meta, err := lambdamicrovms.NewFromConfig(cfg).GetMicrovm(ctx, &lambdamicrovms.GetMicrovmInput{
		MicrovmIdentifier: aws.String(sb.ID()),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{resources.ConnectorARN}, meta.EgressNetworkConnectors,
		"runtime metadata must report exactly the isolated connector and never INTERNET_EGRESS")

	// DNS is recorded, not required to fail — MVP NO_PUBLIC_EGRESS allows it.
	dnsRes, dnsErr := sb.Exec(ctx, []string{"sh", "-c", "getent hosts example.com"})
	switch {
	case dnsErr != nil:
		t.Logf("DNS probe transport error (not a pass/fail signal): %v", dnsErr)
	case dnsRes.ExitCode == 0:
		t.Logf("INFO: DNS resolved (allowed under MVP NO_PUBLIC_EGRESS): %s", strings.TrimSpace(string(dnsRes.Output)))
	default:
		t.Logf("INFO: DNS did not resolve (also allowed under MVP; strict DNS is Task 6.2, not implemented)")
	}

	// Public connections must fail: direct IP and HTTPS-by-hostname.
	httpsRes, err := sb.Exec(ctx, []string{"sh", "-c", "timeout 8 curl -s --connect-timeout 3 https://example.com/ -o /dev/null"})
	require.NoError(t, err, "exec transport must succeed even though the probed connection fails")
	assert.NotEqualf(t, 0, httpsRes.ExitCode, "HTTPS to a public hostname must fail under --egress none: %s", httpsRes.Output)

	directIPRes, err := sb.Exec(ctx, []string{"sh", "-c", "timeout 8 curl -s --connect-timeout 3 http://1.1.1.1/ -o /dev/null"})
	require.NoError(t, err, "exec transport must succeed even though the probed connection fails")
	assert.NotEqualf(t, 0, directIPRes.ExitCode, "direct public IP connection must fail under --egress none: %s", directIPRes.Output)
}
