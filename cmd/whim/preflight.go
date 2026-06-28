package main

import (
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(preflightCmd)
}

var preflightCmd = &cobra.Command{
	Use:   "preflight-check",
	Short: "Verify AWS credentials and required infrastructure for whim",
	Long: `Read-only checks that must pass before 'whim init' or any whim command:

  1. AWS credentials resolve (sts:GetCallerIdentity)
  2. Lambda MicroVMs accessible (list-managed-microvm-images)
  3. IAM build role exists
  4. S3 artifact bucket exists

Exits 0 if all pass, 1 if any fail. Run 'whim init' to provision what's missing.`,
	RunE: runPreflight,
}

func runPreflight(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}

	allOK := true
	check := func(label string, fn func() error) {
		if err := fn(); err != nil {
			printErr(cmd, "  [FAIL] %s: %v\n", label, err)
			allOK = false
		} else {
			printOut(cmd, "  [ OK ] %s\n", label)
		}
	}

	printOut(cmd, "whim preflight-check:\n")

	// 1. Credentials.
	var accountID string
	check("AWS credentials (sts:GetCallerIdentity)", func() error {
		out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
		if err != nil {
			return err
		}
		accountID = aws.ToString(out.Account)
		return nil
	})

	// 2. Lambda MicroVMs access — list managed images is a safe read-only probe.
	check("Lambda MicroVMs access", func() error {
		_, err := lambdamicrovms.NewFromConfig(cfg).ListManagedMicrovmImages(ctx, nil)
		return err
	})

	// 3. IAM build role.
	roleName := defaultBuildRoleName()
	check("IAM build role ("+roleName+")", func() error {
		_, err := iam.NewFromConfig(cfg).GetRole(ctx, &iam.GetRoleInput{
			RoleName: aws.String(roleName),
		})
		return err
	})

	// 4. S3 artifact bucket.
	bucket := defaultBucketName(accountID, cfg.Region)
	check("S3 artifact bucket ("+bucket+")", func() error {
		_, err := s3.NewFromConfig(cfg).HeadBucket(ctx, &s3.HeadBucketInput{
			Bucket: aws.String(bucket),
		})
		return err
	})

	if !allOK {
		return fmt.Errorf("one or more checks failed — run 'whim init' to provision missing resources")
	}
	printOut(cmd, "All checks passed.\n")
	return nil
}
