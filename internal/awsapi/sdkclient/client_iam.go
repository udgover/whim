package sdkclient

import (
	"context"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/udgover/whim/internal/awsapi"
)

// decodeAssumeRolePolicyDocument URL-decodes the trust policy document IAM
// returns from GetRole. IAM percent-encodes this field; if decoding fails
// (already-plain JSON, or malformed input), the raw string is returned
// unchanged rather than dropped, since GetRole has no error return path here.
func decodeAssumeRolePolicyDocument(s string) string {
	decoded, err := url.QueryUnescape(s)
	if err != nil {
		return s
	}
	return decoded
}

func mapAttachedPolicy(p iamtypes.AttachedPolicy) awsapi.AttachedPolicy {
	return awsapi.AttachedPolicy{
		PolicyName: aws.ToString(p.PolicyName),
		PolicyARN:  aws.ToString(p.PolicyArn),
	}
}

// GetRole delegates to IAM GetRole. A missing role is reported as
// awsapi.ErrNotFound. The trust policy document is URL-decoded to plain JSON.
func (c *Client) GetRole(ctx context.Context, in *awsapi.GetRoleInput) (*awsapi.GetRoleOutput, error) {
	out, err := c.iam.GetRole(ctx, &iam.GetRoleInput{
		RoleName: aws.String(in.RoleName),
	})
	if err != nil {
		return nil, mapIAMErr(err)
	}
	return &awsapi.GetRoleOutput{
		ARN:                      aws.ToString(out.Role.Arn),
		RoleName:                 aws.ToString(out.Role.RoleName),
		AssumeRolePolicyDocument: decodeAssumeRolePolicyDocument(aws.ToString(out.Role.AssumeRolePolicyDocument)),
	}, nil
}

// ListAttachedRolePolicies delegates to IAM ListAttachedRolePolicies,
// following pagination to return every attached managed policy.
func (c *Client) ListAttachedRolePolicies(ctx context.Context, in *awsapi.ListAttachedRolePoliciesInput) (*awsapi.ListAttachedRolePoliciesOutput, error) {
	var items []awsapi.AttachedPolicy
	var marker *string
	for {
		out, err := c.iam.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
			RoleName: aws.String(in.RoleName),
			Marker:   marker,
		})
		if err != nil {
			return nil, mapIAMErr(err)
		}
		for _, p := range out.AttachedPolicies {
			items = append(items, mapAttachedPolicy(p))
		}
		if !out.IsTruncated {
			break
		}
		marker = out.Marker
	}
	return &awsapi.ListAttachedRolePoliciesOutput{Items: items}, nil
}

// ListRolePolicies delegates to IAM ListRolePolicies, following pagination
// to return every inline policy name.
func (c *Client) ListRolePolicies(ctx context.Context, in *awsapi.ListRolePoliciesInput) (*awsapi.ListRolePoliciesOutput, error) {
	var names []string
	var marker *string
	for {
		out, err := c.iam.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{
			RoleName: aws.String(in.RoleName),
			Marker:   marker,
		})
		if err != nil {
			return nil, mapIAMErr(err)
		}
		names = append(names, out.PolicyNames...)
		if !out.IsTruncated {
			break
		}
		marker = out.Marker
	}
	return &awsapi.ListRolePoliciesOutput{PolicyNames: names}, nil
}
