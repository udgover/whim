package sdkclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sdktypes "github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
)

// TestSdkCapabilities checks the string→SDK Capability lift used when submitting
// a build. An empty input must produce nil so the request omits the field.
func TestSdkCapabilities(t *testing.T) {
	assert.Nil(t, sdkCapabilities(nil), "nil in → nil out (no field sent)")
	assert.Nil(t, sdkCapabilities([]string{}), "empty in → nil out (no field sent)")

	got := sdkCapabilities([]string{"ALL"})
	assert.Equal(t, []sdktypes.Capability{sdktypes.CapabilityAll}, got)
}

type captureHTTPClient struct {
	body     []byte
	response string
}

func (c *captureHTTPClient) Do(req *http.Request) (*http.Response, error) {
	// req.Body is nil for bodyless requests (e.g. GetNetworkConnector's GET);
	// only the POST-with-body tests need the captured bytes.
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		c.body = body
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(c.response)),
		Request:    req,
	}, nil
}

func TestCreateMicrovmImage_SerializesEgressConnector(t *testing.T) {
	httpClient := &captureHTTPClient{response: `{
		"imageArn":"arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test",
		"imageVersion":"1",
		"state":"CREATING"
	}`}
	client := New(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test-access-key", "test-signing-key", ""),
		HTTPClient:  httpClient,
	})

	_, err := client.CreateMicrovmImage(context.Background(), &awsapi.CreateMicrovmImageInput{
		Name:             "whim-test",
		BaseImageARN:     "arn:aws:lambda:us-east-1:aws:microvm-image:al2023-1",
		CodeArtifactURI:  "s3://bucket/app.zip",
		BuildRoleARN:     "arn:aws:iam::123456789012:role/whim-build",
		EgressConnectors: []string{"arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"},
	})
	require.NoError(t, err)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(httpClient.body, &body))
	raw, ok := body["egressNetworkConnectors"]
	require.True(t, ok, "egress connector ARN must be serialized")
	assert.JSONEq(t, `["arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"]`, string(raw))
}

func TestRunMicrovm_SerializesEgressConnector(t *testing.T) {
	httpClient := &captureHTTPClient{response: `{
		"microvmId":"mvm-test",
		"endpoint":"mvm-test.lambda-microvm.us-east-1.on.aws",
		"state":"PENDING",
		"imageArn":"arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test",
		"imageVersion":"1",
		"maximumDurationInSeconds":1500,
		"startedAt":1784106000
	}`}
	client := New(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test-access-key", "test-signing-key", ""),
		HTTPClient:  httpClient,
	})

	_, err := client.RunMicrovm(context.Background(), &awsapi.RunMicrovmInput{
		ImageIdentifier:          "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test",
		IngressNetworkConnectors: []string{"arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:SHELL_INGRESS"},
		EgressNetworkConnectors:  []string{"arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"},
	})
	require.NoError(t, err)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(httpClient.body, &body))
	raw, ok := body["egressNetworkConnectors"]
	require.True(t, ok, "egress connector ARN must be serialized")
	assert.JSONEq(t, `["arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"]`, string(raw))
}

func TestGetMicrovm_MapsEgressConnectors(t *testing.T) {
	httpClient := &captureHTTPClient{response: `{
		"microvmId":"mvm-test",
		"endpoint":"mvm-test.lambda-microvm.us-east-1.on.aws",
		"state":"RUNNING",
		"imageArn":"arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test",
		"imageVersion":"1",
		"maximumDurationInSeconds":1500,
		"startedAt":1784106000,
		"egressNetworkConnectors":["arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"]
	}`}
	client := New(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test-access-key", "test-signing-key", ""),
		HTTPClient:  httpClient,
	})

	out, err := client.GetMicrovm(context.Background(), &awsapi.GetMicrovmInput{MicrovmIdentifier: "mvm-test"})

	require.NoError(t, err)
	assert.Equal(t,
		[]string{"arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"},
		out.EgressNetworkConnectors,
	)
}

// TestGetNetworkConnector_MapsVpcEgressConfiguration checks that the
// connector's VpcEgressConfiguration subnets/security groups are extracted
// into awsapi's flat SubnetIDs/SecurityGroupIDs fields, since Task 2.4
// (connector-to-VPC topology validation) needs to compare them against the
// managed resource group's expected topology.
func TestGetNetworkConnector_MapsVpcEgressConfiguration(t *testing.T) {
	httpClient := &captureHTTPClient{response: `{
		"Arn":"arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress",
		"Id":"nc-123",
		"Name":"whim-no-public-egress",
		"State":"ACTIVE",
		"Configuration":{
			"VpcEgressConfiguration":{
				"SubnetIds":["subnet-a"],
				"SecurityGroupIds":["sg-a"],
				"NetworkProtocol":"IPv4",
				"AssociatedComputeResourceTypes":["MicroVm"]
			}
		}
	}`}
	client := New(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test-access-key", "test-signing-key", ""),
		HTTPClient:  httpClient,
	})

	out, err := client.GetNetworkConnector(context.Background(), &awsapi.GetNetworkConnectorInput{Identifier: "whim-no-public-egress"})
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", out.State)
	assert.Equal(t, []string{"subnet-a"}, out.SubnetIDs)
	assert.Equal(t, []string{"sg-a"}, out.SecurityGroupIDs)
}

func TestCreateNetworkConnector_SerializesVpcEgressConfiguration(t *testing.T) {
	httpClient := &captureHTTPClient{response: `{
		"arn":"arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress",
		"id":"nc-123",
		"name":"whim-no-egress",
		"state":"PENDING"
	}`}
	client := New(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test-access-key", "test-signing-key", ""),
		HTTPClient:  httpClient,
	})

	_, err := client.CreateNetworkConnector(context.Background(), &awsapi.CreateNetworkConnectorInput{
		Name:             "whim-no-egress",
		SubnetIDs:        []string{"subnet-a"},
		SecurityGroupIDs: []string{"sg-a"},
		OperatorRoleARN:  "arn:aws:iam::123456789012:role/whim-network-operator",
		Tags:             map[string]string{"ManagedBy": "whim"},
	})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(httpClient.body, &body))
	assert.Equal(t, "whim-no-egress", body["Name"])
	assert.Equal(t, "arn:aws:iam::123456789012:role/whim-network-operator", body["OperatorRole"])

	cfg := body["Configuration"].(map[string]any)
	vpc := cfg["VpcEgressConfiguration"].(map[string]any)
	assert.Equal(t, []any{"subnet-a"}, vpc["SubnetIds"])
	assert.Equal(t, []any{"sg-a"}, vpc["SecurityGroupIds"])
	assert.Equal(t, "IPv4", vpc["NetworkProtocol"])
	assert.Equal(t, []any{"MicroVm"}, vpc["AssociatedComputeResourceTypes"])
}
