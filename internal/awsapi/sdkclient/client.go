// Package sdkclient provides a real implementation of awsapi.API that
// delegates to the AWS SDK for Go v2 lambdamicrovms client.
package sdkclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	sdktypes "github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"

	"github.com/udgover/whim/internal/awsapi"
)

// mapErr translates SDK ResourceNotFoundException into awsapi.ErrNotFound so
// callers can match absence with errors.Is. Other errors pass through unchanged.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var nfe *sdktypes.ResourceNotFoundException
	if errors.As(err, &nfe) {
		return fmt.Errorf("%w: %v", awsapi.ErrNotFound, err)
	}
	return err
}

// Client wraps the real lambdamicrovms SDK client and implements awsapi.API.
type Client struct {
	sdk *lambdamicrovms.Client
}

// New constructs a Client from the caller-supplied aws.Config.
func New(cfg aws.Config) *Client {
	return &Client{sdk: lambdamicrovms.NewFromConfig(cfg)}
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
		sdkIn.IdlePolicy = &sdktypes.IdlePolicy{
			AutoResumeEnabled:        aws.Bool(in.IdlePolicy.AutoResumeEnabled),
			MaxIdleDurationSeconds:   aws.Int32(in.IdlePolicy.MaxIdleDurationSeconds),
			SuspendedDurationSeconds: aws.Int32(in.IdlePolicy.SuspendedDurationSeconds),
		}
	}
	out, err := c.sdk.RunMicrovm(ctx, sdkIn)
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
	out, err := c.sdk.GetMicrovm(ctx, &lambdamicrovms.GetMicrovmInput{
		MicrovmIdentifier: aws.String(in.MicrovmIdentifier),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &awsapi.GetMicrovmOutput{
		MicrovmID: aws.ToString(out.MicrovmId),
		Endpoint:  aws.ToString(out.Endpoint),
		State:     string(out.State),
	}, nil
}

// TerminateMicrovm delegates to the SDK and discards the empty output.
func (c *Client) TerminateMicrovm(ctx context.Context, in *awsapi.TerminateMicrovmInput) error {
	_, err := c.sdk.TerminateMicrovm(ctx, &lambdamicrovms.TerminateMicrovmInput{
		MicrovmIdentifier: aws.String(in.MicrovmIdentifier),
	})
	return mapErr(err)
}

// SuspendMicrovm delegates to the SDK; a not-found VM is reported as awsapi.ErrNotFound.
func (c *Client) SuspendMicrovm(ctx context.Context, in *awsapi.SuspendMicrovmInput) error {
	_, err := c.sdk.SuspendMicrovm(ctx, &lambdamicrovms.SuspendMicrovmInput{
		MicrovmIdentifier: aws.String(in.MicrovmIdentifier),
	})
	return mapErr(err)
}

// ResumeMicrovm delegates to the SDK; a not-found VM is reported as awsapi.ErrNotFound.
func (c *Client) ResumeMicrovm(ctx context.Context, in *awsapi.ResumeMicrovmInput) error {
	_, err := c.sdk.ResumeMicrovm(ctx, &lambdamicrovms.ResumeMicrovmInput{
		MicrovmIdentifier: aws.String(in.MicrovmIdentifier),
	})
	return mapErr(err)
}

// CreateShellAuthToken mints a shell auth token and extracts the X-aws-proxy-auth header value.
func (c *Client) CreateShellAuthToken(ctx context.Context, in *awsapi.CreateShellAuthTokenInput) (*awsapi.CreateShellAuthTokenOutput, error) {
	out, err := c.sdk.CreateMicrovmShellAuthToken(ctx, &lambdamicrovms.CreateMicrovmShellAuthTokenInput{
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
		out, err := c.sdk.ListMicrovms(ctx, &lambdamicrovms.ListMicrovmsInput{
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

// CreateMicrovmImage delegates to the SDK, wrapping the code artifact as a URI union member.
func (c *Client) CreateMicrovmImage(ctx context.Context, in *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
	out, err := c.sdk.CreateMicrovmImage(ctx, &lambdamicrovms.CreateMicrovmImageInput{
		Name:                    aws.String(in.Name),
		BaseImageArn:            aws.String(in.BaseImageARN),
		BuildRoleArn:            aws.String(in.BuildRoleARN),
		CodeArtifact:            &sdktypes.CodeArtifactMemberUri{Value: in.CodeArtifactURI},
		EgressNetworkConnectors: in.EgressConnectors,
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

// GetMicrovmImage delegates to the SDK and maps the image state fields.
// A not-found image is reported as awsapi.ErrNotFound.
func (c *Client) GetMicrovmImage(ctx context.Context, in *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
	out, err := c.sdk.GetMicrovmImage(ctx, &lambdamicrovms.GetMicrovmImageInput{
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
	_, err := c.sdk.DeleteMicrovmImage(ctx, &lambdamicrovms.DeleteMicrovmImageInput{
		ImageIdentifier: aws.String(in.ImageIdentifier),
	})
	return mapErr(err)
}

// ListMicrovmImages delegates to the SDK, following pagination to return all images.
func (c *Client) ListMicrovmImages(ctx context.Context, in *awsapi.ListMicrovmImagesInput) (*awsapi.ListMicrovmImagesOutput, error) {
	var items []awsapi.MicrovmImageSummary
	var token *string
	for {
		out, err := c.sdk.ListMicrovmImages(ctx, &lambdamicrovms.ListMicrovmImagesInput{
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
