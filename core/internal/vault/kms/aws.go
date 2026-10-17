package kms

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// AWSKey is a symmetric AWS KMS key. Credentials and region come the usual
// AWS way: environment, shared config, IAM roles for service accounts or the
// instance's role. The associated data goes in the encryption context, which
// CloudTrail logs with every decrypt.
type AWSKey struct {
	client *awskms.Client
	arn    string
}

// NewAWS uses the key key: an id, ARN, alias name or alias ARN.
func NewAWS(ctx context.Context, key string, opts ...func(*awskms.Options)) (*AWSKey, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("AWS configuration: %w", err)
	}
	return newAWS(ctx, awskms.NewFromConfig(cfg, opts...), key)
}

func newAWS(ctx context.Context, client *awskms.Client, key string) (*AWSKey, error) {
	out, err := client.DescribeKey(ctx, &awskms.DescribeKeyInput{KeyId: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("AWS KMS key %s: %w", key, err)
	}
	m := out.KeyMetadata
	if m.KeySpec != types.KeySpecSymmetricDefault || m.KeyUsage != types.KeyUsageTypeEncryptDecrypt {
		return nil, fmt.Errorf("AWS KMS key %s must be a symmetric encrypt and decrypt key, not %s for %s", key, m.KeySpec, m.KeyUsage)
	}
	if !m.Enabled {
		return nil, fmt.Errorf("AWS KMS key %s is disabled", key)
	}
	return &AWSKey{client: client, arn: aws.ToString(m.Arn)}, nil
}

func (k *AWSKey) KeyID() string          { return AWS + ":" + k.arn }
func (k *AWSKey) Owns(keyID string) bool { return keyID == k.KeyID() }

func awsContext(aad []byte) map[string]string {
	return map[string]string{"grantra": base64.StdEncoding.EncodeToString(aad)}
}

func (k *AWSKey) Wrap(dataKey, aad []byte) ([]byte, error) {
	ctx, cancel := callCtx()
	defer cancel()
	out, err := k.client.Encrypt(ctx, &awskms.EncryptInput{KeyId: aws.String(k.arn), Plaintext: dataKey, EncryptionContext: awsContext(aad)})
	if err != nil {
		return nil, fmt.Errorf("AWS KMS encrypt: %w", err)
	}
	if len(out.CiphertextBlob) == 0 {
		return nil, errEmpty
	}
	return out.CiphertextBlob, nil
}

func (k *AWSKey) Unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	if !k.Owns(keyID) {
		return nil, errNotOwned(keyID, k.KeyID())
	}
	key, _ := keyOf(AWS, keyID)
	ctx, cancel := callCtx()
	defer cancel()
	// Naming the key makes KMS refuse a ciphertext of any other key.
	out, err := k.client.Decrypt(ctx, &awskms.DecryptInput{KeyId: aws.String(key), CiphertextBlob: wrapped, EncryptionContext: awsContext(aad)})
	if err != nil {
		return nil, errUnwrap(AWS, err)
	}
	return out.Plaintext, nil
}
