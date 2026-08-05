package sdkclient

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"

	"github.com/udgover/whim/internal/awsapi"
)

// TestDecodeAssumeRolePolicyDocument_URLEncoded checks the common case: IAM
// returns AssumeRolePolicyDocument percent-encoded, and validation code
// downstream needs plain JSON to unmarshal.
func TestDecodeAssumeRolePolicyDocument_URLEncoded(t *testing.T) {
	encoded := `%7B%22Version%22%3A%222012-10-17%22%7D`
	assert.Equal(t, `{"Version":"2012-10-17"}`, decodeAssumeRolePolicyDocument(encoded))
}

// TestDecodeAssumeRolePolicyDocument_FallsBackOnInvalidEncoding checks that
// malformed percent-encoding doesn't drop the document; it returns the raw
// string instead of an error, since GetRole has no error return path here.
func TestDecodeAssumeRolePolicyDocument_FallsBackOnInvalidEncoding(t *testing.T) {
	raw := `{"Version":"2012-10-17"}` // already-plain JSON, not percent-encoded
	assert.Equal(t, raw, decodeAssumeRolePolicyDocument(raw))
}

func TestMapAttachedPolicy(t *testing.T) {
	got := mapAttachedPolicy(iamtypes.AttachedPolicy{
		PolicyName: aws.String("AWSLambdaNetworkConnectorOperatorPolicy"),
		PolicyArn:  aws.String("arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy"),
	})
	assert.Equal(t, awsapi.AttachedPolicy{
		PolicyName: "AWSLambdaNetworkConnectorOperatorPolicy",
		PolicyARN:  "arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy",
	}, got)
}
