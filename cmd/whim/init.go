package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/udgover/whim/microvm"
)

// defaultImageName is the name of the image whim builds and uses by default.
const defaultImageName = "whim-default"

// imageBuildTimeout bounds the whole init flow (build + polling) so a stuck
// AWS state can't hang the CLI indefinitely. Builds normally take ~2-3 minutes.
const imageBuildTimeout = 15 * time.Minute

func init() {
	initCmd.Flags().String("image-name", defaultImageName,
		"image name to build under (a custom name tests the build/run pipeline without touching the default; content is the same default Dockerfile)")
	initCmd.Flags().Bool("force", false,
		"delete and rebuild the image even if it already exists (destructive)")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Provision build infrastructure and build the default whim image",
	Long: `whim init performs one-time bootstrap:

  1. Create (or verify) an S3 bucket for image build artifacts
  2. Create (or verify) an IAM build role for Lambda to assume
  3. Fetch the managed AL2023 base image ARN
  4. Build the default whim image (~2-3 minutes)
  5. Cache the resulting image ARN in ~/.config/whim/config.json

The cached ARN is used by 'whim shell', 'whim run', etc. as the default image.`,
	RunE: runInit,
}

func runInit(cmd *cobra.Command, _ []string) error {
	// Bound the whole flow so a stuck CREATING/DELETING state can't hang forever.
	ctx, cancel := context.WithTimeout(cmd.Context(), imageBuildTimeout)
	defer cancel()

	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	imageName, _ := cmd.Flags().GetString("image-name")
	force, _ := cmd.Flags().GetBool("force")

	// Resolve account ID — needed for resource naming and ARN construction.
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	if err != nil {
		return fmt.Errorf("resolve account ID: %w", err)
	}
	accountID := aws.ToString(out.Account)
	region := cfg.Region

	printOut(cmd, "Initialising whim in account %s / region %s\n", accountID, region)

	// Step 1: S3 bucket.
	bucket := defaultBucketName(accountID, region)
	if err := ensureBucket(ctx, cfg, bucket, region, cmd); err != nil {
		return err
	}

	// Step 2: IAM build role.
	roleName := defaultBuildRoleName()
	roleARN, err := ensureBuildRole(ctx, cfg, roleName, bucket, cmd)
	if err != nil {
		return err
	}

	// Step 3: Managed base image ARN.
	baseARN, err := managedBaseImageARN(ctx, cfg)
	if err != nil {
		return fmt.Errorf("list managed images: %w", err)
	}
	printOut(cmd, "  Using base image: %s\n", baseARN)

	// Step 4: Upload the Dockerfile zip (keyed by image name) and build/reuse.
	artifactKey := imageName + ".zip"
	if err := uploadDefaultDockerfile(ctx, cfg, bucket, artifactKey); err != nil {
		return fmt.Errorf("upload Dockerfile: %w", err)
	}

	mgr := microvm.NewFromConfig(cfg,
		microvm.WithAccountID(accountID),
	)
	spec := microvm.ImageSpec{
		Name:            imageName,
		BaseImageARN:    baseARN,
		CodeArtifactURI: fmt.Sprintf("s3://%s/%s", bucket, artifactKey),
		BuildRoleARN:    roleARN,
		Egress:          microvm.EgressPublic,
	}

	var imageARN string
	if force {
		printOut(cmd, "  Force: deleting then rebuilding image %q…\n", imageName)
		imageARN, err = mgr.ForceRebuildImage(ctx, spec)
	} else {
		printOut(cmd, "  Building image %q (this takes ~2-3 minutes if not cached)…\n", imageName)
		imageARN, err = mgr.EnsureImage(ctx, spec)
	}
	if err != nil {
		return fmt.Errorf("build image: %w", err)
	}
	printOut(cmd, "  Image ready: %s\n", imageARN)

	// Step 5: Cache as the active default only when building the default image —
	// a custom --image-name builds without disturbing the active default.
	if imageName == defaultImageName {
		if err := SaveConfig(&Config{ImageARN: imageARN}); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		printOut(cmd, "  Cached to: %s\n", ConfigPath())
		printOut(cmd, "whim init complete. Run 'whim' to start a shell.\n")
	} else {
		printOut(cmd, "  Custom image built; active default unchanged.\n")
	}
	return nil
}

// buildAWSConfig loads the default AWS config, honouring --region and --profile.
func buildAWSConfig(ctx context.Context, cmd *cobra.Command) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region, _ := cmd.Flags().GetString("region"); region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if profile, _ := cmd.Flags().GetString("profile"); profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return cfg, err
	}
	if cfg.Region == "" {
		return cfg, errors.New("no AWS region configured — pass --region or set AWS_REGION / a profile region")
	}
	return cfg, nil
}

// defaultBucketName returns the deterministic S3 bucket name for whim artifacts.
func defaultBucketName(accountID, region string) string {
	return fmt.Sprintf("whim-artifacts-%s-%s", accountID, region)
}

// defaultBuildRoleName returns the IAM build role name (account-scoped, fixed).
func defaultBuildRoleName() string {
	return "whim-build-role"
}

func ensureBucket(ctx context.Context, cfg aws.Config, bucket, region string, cmd *cobra.Command) error {
	s3c := s3.NewFromConfig(cfg)
	_, err := s3c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		printOut(cmd, "  S3 bucket exists: %s\n", bucket)
		return nil
	}
	// Only treat a genuine 404 as "create it"; surface permission/transient errors.
	var notFound *s3types.NotFound
	if !errors.As(err, &notFound) {
		return fmt.Errorf("check S3 bucket %q: %w", bucket, err)
	}

	in := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	// us-east-1 must NOT specify a LocationConstraint.
	if region != "us-east-1" {
		in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(region),
		}
	}
	if _, err := s3c.CreateBucket(ctx, in); err != nil {
		return fmt.Errorf("create S3 bucket %q: %w", bucket, err)
	}

	// Block all public access on the artifacts bucket.
	if _, err := s3c.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	}); err != nil {
		return fmt.Errorf("block public access on bucket %q: %w", bucket, err)
	}
	printOut(cmd, "  Created S3 bucket: %s\n", bucket)
	return nil
}

func ensureBuildRole(ctx context.Context, cfg aws.Config, roleName, bucket string, cmd *cobra.Command) (string, error) {
	iamc := iam.NewFromConfig(cfg)

	// Get-or-create the role.
	var roleARN string
	out, err := iamc.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
	switch {
	case err == nil:
		roleARN = aws.ToString(out.Role.Arn)
		printOut(cmd, "  IAM role exists: %s\n", roleName)
	case isNoSuchEntity(err):
		trustPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`
		created, cerr := iamc.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName:                 aws.String(roleName),
			AssumeRolePolicyDocument: aws.String(trustPolicy),
			Description:              aws.String("whim image build role"),
		})
		if cerr != nil {
			return "", fmt.Errorf("create IAM role %q: %w", roleName, cerr)
		}
		roleARN = aws.ToString(created.Role.Arn)
		printOut(cmd, "  Created IAM role: %s\n", roleARN)
	default:
		return "", fmt.Errorf("check IAM role %q: %w", roleName, err)
	}

	// Always (re)attach the inline policy. PutRolePolicy is idempotent, so this
	// also repairs a role left without a policy by an earlier partial run.
	permPolicy, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{"Effect": "Allow", "Action": []string{"s3:GetObject"}, "Resource": "arn:aws:s3:::" + bucket + "/*"},
			{"Effect": "Allow", "Action": []string{"logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"}, "Resource": "arn:aws:logs:*:*:*"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal build-role policy: %w", err)
	}
	if _, err := iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String("whim-build"),
		PolicyDocument: aws.String(string(permPolicy)),
	}); err != nil {
		return "", fmt.Errorf("attach policy to role %q: %w", roleName, err)
	}
	return roleARN, nil
}

// isNoSuchEntity reports whether err is an IAM NoSuchEntity (resource absent).
func isNoSuchEntity(err error) bool {
	var nse *iamtypes.NoSuchEntityException
	return errors.As(err, &nse)
}

// managedBaseImageARN returns the first available managed base image ARN.
func managedBaseImageARN(ctx context.Context, cfg aws.Config) (string, error) {
	out, err := lambdamicrovms.NewFromConfig(cfg).ListManagedMicrovmImages(ctx, nil)
	if err != nil {
		return "", err
	}
	if len(out.Items) == 0 {
		return "", fmt.Errorf("no managed base images available in this region")
	}
	// Deterministic selection: prefer an Amazon Linux 2023 base, else the
	// lexicographically-first ARN (stable across calls regardless of list order).
	arns := make([]string, 0, len(out.Items))
	for _, it := range out.Items {
		arns = append(arns, aws.ToString(it.ImageArn))
	}
	sort.Strings(arns)
	for _, a := range arns {
		if strings.Contains(a, "al2023") {
			return a, nil
		}
	}
	return arns[0], nil
}

// uploadDefaultDockerfile packages the minimal shell-sandbox Dockerfile into a
// ZIP archive and uploads it to S3. tar+gzip are installed so `whim put`/`get`
// (tar.gz over the pty) work — the base AL2023 image ships without tar.
func uploadDefaultDockerfile(ctx context.Context, cfg aws.Config, bucket, key string) error {
	const dockerfile = "FROM public.ecr.aws/amazonlinux/amazonlinux:2023\n" +
		"RUN dnf install -y tar gzip && dnf clean all\n" +
		"CMD [\"sleep\", \"infinity\"]\n"
	zipData, err := buildZip("Dockerfile", []byte(dockerfile))
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

// buildZip creates an in-memory ZIP archive containing a single file.
func buildZip(filename string, content []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create(filename)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(content); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
