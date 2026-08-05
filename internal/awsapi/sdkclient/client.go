// Package sdkclient provides a real implementation of awsapi.API that
// delegates to the AWS SDK for Go v2 lambdamicrovms client.
package sdkclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambdacore"
	coretypes "github.com/aws/aws-sdk-go-v2/service/lambdacore/types"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	microvmtypes "github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"
	"github.com/aws/smithy-go"

	"github.com/udgover/whim/internal/awsapi"
)

// mapErr translates SDK ResourceNotFoundException into awsapi.ErrNotFound so
// callers can match absence with errors.Is. Other errors pass through unchanged.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var microvmNFE *microvmtypes.ResourceNotFoundException
	if errors.As(err, &microvmNFE) {
		return fmt.Errorf("%w: %v", awsapi.ErrNotFound, err)
	}
	var coreNFE *coretypes.ResourceNotFoundException
	if errors.As(err, &coreNFE) {
		return fmt.Errorf("%w: %v", awsapi.ErrNotFound, err)
	}
	return err
}

// mapEC2Err translates EC2 "not found" errors into awsapi.ErrNotFound. EC2
// reports absence as a generic API error (e.g. InvalidVpcID.NotFound,
// InvalidSubnetID.NotFound, InvalidGroup.NotFound) rather than a single
// typed exception, so this matches on the ErrorCode suffix common to all of
// them instead of a fixed list of exception types.
func mapEC2Err(err error) error {
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && strings.HasSuffix(apiErr.ErrorCode(), "NotFound") {
		return fmt.Errorf("%w: %v", awsapi.ErrNotFound, err)
	}
	return err
}

// mapIAMErr translates IAM's NoSuchEntityException into awsapi.ErrNotFound.
func mapIAMErr(err error) error {
	if err == nil {
		return nil
	}
	var nse *iamtypes.NoSuchEntityException
	if errors.As(err, &nse) {
		return fmt.Errorf("%w: %v", awsapi.ErrNotFound, err)
	}
	return err
}

// Client wraps the real Lambda MicroVMs, Lambda Core, EC2, and IAM SDK
// clients and implements awsapi.API.
type Client struct {
	microvms *lambdamicrovms.Client
	core     *lambdacore.Client
	ec2      *ec2.Client
	iam      *iam.Client
}

// New constructs a Client from the caller-supplied aws.Config.
func New(cfg aws.Config) *Client {
	return &Client{
		microvms: lambdamicrovms.NewFromConfig(cfg),
		core:     lambdacore.NewFromConfig(cfg),
		ec2:      ec2.NewFromConfig(cfg),
		iam:      iam.NewFromConfig(cfg),
	}
}

// Ensure Client satisfies the interface at compile time.
var _ awsapi.API = (*Client)(nil)

// RunMicrovm delegates to the SDK and maps types.
func (c *Client) RunMicrovm(ctx context.Context, in *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
	sdkIn := &lambdamicrovms.RunMicrovmInput{
		ImageIdentifier:          aws.String(in.ImageIdentifier),
		ImageVersion:             in.ImageVersion,
		IngressNetworkConnectors: in.IngressNetworkConnectors,
		EgressNetworkConnectors:  in.EgressNetworkConnectors,
		MaximumDurationInSeconds: in.MaximumDurationInSeconds,
		ExecutionRoleArn:         in.ExecutionRoleARN,
	}
	if in.IdlePolicy != nil {
		sdkIn.IdlePolicy = &microvmtypes.IdlePolicy{
			AutoResumeEnabled:        aws.Bool(in.IdlePolicy.AutoResumeEnabled),
			MaxIdleDurationSeconds:   aws.Int32(in.IdlePolicy.MaxIdleDurationSeconds),
			SuspendedDurationSeconds: aws.Int32(in.IdlePolicy.SuspendedDurationSeconds),
		}
	}
	out, err := c.microvms.RunMicrovm(ctx, sdkIn)
	if err != nil {
		return nil, err
	}
	return &awsapi.RunMicrovmOutput{
		MicrovmID: aws.ToString(out.MicrovmId),
		Endpoint:  aws.ToString(out.Endpoint),
		State:     string(out.State),
	}, nil
}

// GetMicrovm delegates to the SDK and maps types.
func (c *Client) GetMicrovm(ctx context.Context, in *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
	out, err := c.microvms.GetMicrovm(ctx, &lambdamicrovms.GetMicrovmInput{
		MicrovmIdentifier: aws.String(in.MicrovmIdentifier),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &awsapi.GetMicrovmOutput{
		MicrovmID:               aws.ToString(out.MicrovmId),
		Endpoint:                aws.ToString(out.Endpoint),
		State:                   string(out.State),
		EgressNetworkConnectors: append([]string(nil), out.EgressNetworkConnectors...),
	}, nil
}

// TerminateMicrovm delegates to the SDK and discards the empty output.
func (c *Client) TerminateMicrovm(ctx context.Context, in *awsapi.TerminateMicrovmInput) error {
	_, err := c.microvms.TerminateMicrovm(ctx, &lambdamicrovms.TerminateMicrovmInput{
		MicrovmIdentifier: aws.String(in.MicrovmIdentifier),
	})
	return mapErr(err)
}

// SuspendMicrovm delegates to the SDK; a not-found VM is reported as awsapi.ErrNotFound.
func (c *Client) SuspendMicrovm(ctx context.Context, in *awsapi.SuspendMicrovmInput) error {
	_, err := c.microvms.SuspendMicrovm(ctx, &lambdamicrovms.SuspendMicrovmInput{
		MicrovmIdentifier: aws.String(in.MicrovmIdentifier),
	})
	return mapErr(err)
}

// ResumeMicrovm delegates to the SDK; a not-found VM is reported as awsapi.ErrNotFound.
func (c *Client) ResumeMicrovm(ctx context.Context, in *awsapi.ResumeMicrovmInput) error {
	_, err := c.microvms.ResumeMicrovm(ctx, &lambdamicrovms.ResumeMicrovmInput{
		MicrovmIdentifier: aws.String(in.MicrovmIdentifier),
	})
	return mapErr(err)
}

// CreateShellAuthToken mints a shell auth token and extracts the X-aws-proxy-auth header value.
func (c *Client) CreateShellAuthToken(ctx context.Context, in *awsapi.CreateShellAuthTokenInput) (*awsapi.CreateShellAuthTokenOutput, error) {
	out, err := c.microvms.CreateMicrovmShellAuthToken(ctx, &lambdamicrovms.CreateMicrovmShellAuthTokenInput{
		MicrovmIdentifier:   aws.String(in.MicrovmIdentifier),
		ExpirationInMinutes: aws.Int32(in.ExpirationMinutes),
	})
	if err != nil {
		return nil, err
	}
	// The SDK returns authToken as a map[string]string keyed by header name.
	const headerKey = "X-aws-proxy-auth"
	val, ok := out.AuthToken[headerKey]
	if !ok {
		return nil, fmt.Errorf("sdkclient: X-aws-proxy-auth missing from shell auth token response")
	}
	return &awsapi.CreateShellAuthTokenOutput{
		HeaderKey:   headerKey,
		HeaderValue: val,
	}, nil
}

// ListMicrovms delegates to the SDK, following pagination to return all VMs.
func (c *Client) ListMicrovms(ctx context.Context, in *awsapi.ListMicrovmsInput) (*awsapi.ListMicrovmsOutput, error) {
	var items []awsapi.MicrovmSummary
	var token *string
	for {
		out, err := c.microvms.ListMicrovms(ctx, &lambdamicrovms.ListMicrovmsInput{
			ImageIdentifier: in.ImageIdentifier,
			NextToken:       token,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range out.Items {
			s := awsapi.MicrovmSummary{
				MicrovmID: aws.ToString(item.MicrovmId),
				ImageARN:  aws.ToString(item.ImageArn),
				State:     string(item.State),
			}
			if item.StartedAt != nil {
				s.StartedAt = *item.StartedAt
			}
			items = append(items, s)
		}
		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		token = out.NextToken
	}
	return &awsapi.ListMicrovmsOutput{Items: items}, nil
}

// sdkCapabilities lifts the plain capability strings carried across the awsapi
// boundary into the SDK's typed Capability slice. Returns nil for an empty
// input so the request omits additionalOsCapabilities entirely.
func sdkCapabilities(caps []string) []microvmtypes.Capability {
	if len(caps) == 0 {
		return nil
	}
	out := make([]microvmtypes.Capability, len(caps))
	for i, c := range caps {
		out[i] = microvmtypes.Capability(c)
	}
	return out
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

// CreateMicrovmImage delegates to the SDK, wrapping the code artifact as a URI union member.
func (c *Client) CreateMicrovmImage(ctx context.Context, in *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
	out, err := c.microvms.CreateMicrovmImage(ctx, &lambdamicrovms.CreateMicrovmImageInput{
		Name:                     aws.String(in.Name),
		BaseImageArn:             aws.String(in.BaseImageARN),
		BuildRoleArn:             aws.String(in.BuildRoleARN),
		CodeArtifact:             &microvmtypes.CodeArtifactMemberUri{Value: in.CodeArtifactURI},
		EgressNetworkConnectors:  in.EgressConnectors,
		AdditionalOsCapabilities: sdkCapabilities(in.Capabilities),
	})
	if err != nil {
		return nil, err
	}
	return &awsapi.CreateMicrovmImageOutput{
		ImageARN:     aws.ToString(out.ImageArn),
		ImageVersion: aws.ToString(out.ImageVersion),
		State:        string(out.State),
	}, nil
}

// GetMicrovmImageVersion delegates to the SDK and returns the version's
// additionalOsCapabilities as plain strings. A not-found version is reported as
// awsapi.ErrNotFound.
func (c *Client) GetMicrovmImageVersion(ctx context.Context, in *awsapi.GetMicrovmImageVersionInput) (*awsapi.GetMicrovmImageVersionOutput, error) {
	out, err := c.microvms.GetMicrovmImageVersion(ctx, &lambdamicrovms.GetMicrovmImageVersionInput{
		ImageIdentifier: aws.String(in.ImageIdentifier),
		ImageVersion:    aws.String(in.ImageVersion),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	caps := make([]string, len(out.AdditionalOsCapabilities))
	for i, c := range out.AdditionalOsCapabilities {
		caps[i] = string(c)
	}
	return &awsapi.GetMicrovmImageVersionOutput{
		Capabilities:     caps,
		EgressConnectors: out.EgressNetworkConnectors,
	}, nil
}

// ListNetworkConnectors delegates to Lambda Core, following pagination to
// return all network connectors visible in the account/region.
func (c *Client) ListNetworkConnectors(ctx context.Context, in *awsapi.ListNetworkConnectorsInput) (*awsapi.ListNetworkConnectorsOutput, error) {
	sdkIn := &lambdacore.ListNetworkConnectorsInput{}
	if in.State != "" {
		sdkIn.State = coretypes.NetworkConnectorState(in.State)
	}
	p := lambdacore.NewListNetworkConnectorsPaginator(c.core, sdkIn)
	var items []awsapi.NetworkConnectorSummary
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range out.NetworkConnectors {
			items = append(items, awsapi.NetworkConnectorSummary{
				ARN:   aws.ToString(item.Arn),
				Name:  aws.ToString(item.Name),
				State: string(item.State),
				Type:  string(item.Type),
			})
		}
	}
	return &awsapi.ListNetworkConnectorsOutput{Items: items}, nil
}

// GetNetworkConnector delegates to Lambda Core.
func (c *Client) GetNetworkConnector(ctx context.Context, in *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
	out, err := c.core.GetNetworkConnector(ctx, &lambdacore.GetNetworkConnectorInput{
		Identifier: aws.String(in.Identifier),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	result := &awsapi.GetNetworkConnectorOutput{
		ARN:             aws.ToString(out.Arn),
		Name:            aws.ToString(out.Name),
		State:           string(out.State),
		StateReason:     aws.ToString(out.StateReason),
		StateReasonCode: string(out.StateReasonCode),
	}
	if vpc, ok := out.Configuration.(*coretypes.NetworkConnectorConfigurationMemberVpcEgressConfiguration); ok {
		result.SubnetIDs = vpc.Value.SubnetIds
		result.SecurityGroupIDs = vpc.Value.SecurityGroupIds
	}
	return result, nil
}

// CreateNetworkConnector creates a Lambda Core VPC egress connector for
// MicroVMs. It intentionally exposes only the documented fields whim needs.
func (c *Client) CreateNetworkConnector(ctx context.Context, in *awsapi.CreateNetworkConnectorInput) (*awsapi.CreateNetworkConnectorOutput, error) {
	out, err := c.core.CreateNetworkConnector(ctx, &lambdacore.CreateNetworkConnectorInput{
		Name: aws.String(in.Name),
		Configuration: &coretypes.NetworkConnectorConfigurationMemberVpcEgressConfiguration{
			Value: coretypes.NetworkConnectorVpcEgressConfiguration{
				SubnetIds:        in.SubnetIDs,
				SecurityGroupIds: in.SecurityGroupIDs,
				NetworkProtocol:  coretypes.NetworkProtocolIPv4,
				AssociatedComputeResourceTypes: []coretypes.ComputeResourceType{
					coretypes.ComputeResourceTypeMicroVm,
				},
			},
		},
		OperatorRole: optionalString(in.OperatorRoleARN),
		Tags:         in.Tags,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &awsapi.CreateNetworkConnectorOutput{
		ARN:   aws.ToString(out.Arn),
		Name:  aws.ToString(out.Name),
		State: string(out.State),
	}, nil
}

// GetMicrovmImage delegates to the SDK and maps the image state fields.
// A not-found image is reported as awsapi.ErrNotFound.
func (c *Client) GetMicrovmImage(ctx context.Context, in *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
	out, err := c.microvms.GetMicrovmImage(ctx, &lambdamicrovms.GetMicrovmImageInput{
		ImageIdentifier: aws.String(in.ImageIdentifier),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &awsapi.GetMicrovmImageOutput{
		ImageARN:                 aws.ToString(out.ImageArn),
		Name:                     aws.ToString(out.Name),
		State:                    string(out.State),
		LatestActiveImageVersion: aws.ToString(out.LatestActiveImageVersion),
	}, nil
}

// DeleteMicrovmImage deletes an image; a not-found image is reported as awsapi.ErrNotFound.
func (c *Client) DeleteMicrovmImage(ctx context.Context, in *awsapi.DeleteMicrovmImageInput) error {
	_, err := c.microvms.DeleteMicrovmImage(ctx, &lambdamicrovms.DeleteMicrovmImageInput{
		ImageIdentifier: aws.String(in.ImageIdentifier),
	})
	return mapErr(err)
}

// ListMicrovmImages delegates to the SDK, following pagination to return all images.
func (c *Client) ListMicrovmImages(ctx context.Context, in *awsapi.ListMicrovmImagesInput) (*awsapi.ListMicrovmImagesOutput, error) {
	var items []awsapi.MicrovmImageSummary
	var token *string
	for {
		out, err := c.microvms.ListMicrovmImages(ctx, &lambdamicrovms.ListMicrovmImagesInput{
			NameFilter: in.NameFilter,
			NextToken:  token,
		})
		if err != nil {
			return nil, err
		}
		for _, it := range out.Items {
			s := awsapi.MicrovmImageSummary{
				Name:                     aws.ToString(it.Name),
				ImageARN:                 aws.ToString(it.ImageArn),
				State:                    string(it.State),
				LatestActiveImageVersion: aws.ToString(it.LatestActiveImageVersion),
			}
			if it.CreatedAt != nil {
				s.CreatedAt = *it.CreatedAt
			}
			items = append(items, s)
		}
		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		token = out.NextToken
	}
	return &awsapi.ListMicrovmImagesOutput{Items: items}, nil
}
