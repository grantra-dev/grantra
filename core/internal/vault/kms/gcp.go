package kms

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"

	"golang.org/x/oauth2/google"
)

// GCPKey is a symmetric Google Cloud KMS key (software, HSM or external).
// Credentials come from Application Default Credentials: workload identity,
// the instance's service account or GOOGLE_APPLICATION_CREDENTIALS.
type GCPKey struct {
	hc       *http.Client
	endpoint string
	name     string
}

var gcpKeyName = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+$`)

// NewGCP uses the key name, projects/P/locations/L/keyRings/R/cryptoKeys/K.
// New data keys are wrapped with its primary version.
func NewGCP(ctx context.Context, name string) (*GCPKey, error) {
	hc, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloudkms")
	if err != nil {
		return nil, fmt.Errorf("Google Cloud credentials: %w", err)
	}
	return newGCP(ctx, hc, "https://cloudkms.googleapis.com/v1/", name)
}

func newGCP(ctx context.Context, hc *http.Client, endpoint, name string) (*GCPKey, error) {
	if !gcpKeyName.MatchString(name) {
		return nil, fmt.Errorf("Google Cloud KMS key must be projects/P/locations/L/keyRings/R/cryptoKeys/K, not %q", name)
	}
	k := &GCPKey{hc: hc, endpoint: endpoint, name: name}
	var key struct {
		Purpose string `json:"purpose"`
		Primary *struct {
			State string `json:"state"`
		} `json:"primary"`
	}
	if err := callJSON(ctx, hc, http.MethodGet, endpoint+name, nil, nil, &key); err != nil {
		return nil, fmt.Errorf("Google Cloud KMS key %s: %w", name, err)
	}
	if key.Purpose != "ENCRYPT_DECRYPT" {
		return nil, fmt.Errorf("Google Cloud KMS key %s must be a symmetric encrypt and decrypt key, not %s", name, key.Purpose)
	}
	if key.Primary == nil || key.Primary.State != "ENABLED" {
		return nil, fmt.Errorf("Google Cloud KMS key %s has no enabled primary version", name)
	}
	return k, nil
}

func (k *GCPKey) KeyID() string          { return GCP + ":" + k.name }
func (k *GCPKey) Owns(keyID string) bool { return keyID == k.KeyID() }

func (k *GCPKey) Wrap(dataKey, aad []byte) ([]byte, error) {
	ctx, cancel := callCtx()
	defer cancel()
	var out struct {
		Ciphertext string `json:"ciphertext"`
	}
	in := map[string]string{"plaintext": b64(dataKey), "additionalAuthenticatedData": b64(aad)}
	if err := callJSON(ctx, k.hc, http.MethodPost, k.endpoint+k.name+":encrypt", nil, in, &out); err != nil {
		return nil, fmt.Errorf("Google Cloud KMS encrypt: %w", err)
	}
	wrapped, err := base64.StdEncoding.DecodeString(out.Ciphertext)
	if err != nil || len(wrapped) == 0 {
		return nil, errEmpty
	}
	return wrapped, nil
}

func (k *GCPKey) Unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	if !k.Owns(keyID) {
		return nil, errNotOwned(keyID, k.KeyID())
	}
	name := k.name
	ctx, cancel := callCtx()
	defer cancel()
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	in := map[string]string{"ciphertext": b64(wrapped), "additionalAuthenticatedData": b64(aad)}
	if err := callJSON(ctx, k.hc, http.MethodPost, k.endpoint+name+":decrypt", nil, in, &out); err != nil {
		return nil, errUnwrap(GCP, err)
	}
	return base64.StdEncoding.DecodeString(out.Plaintext)
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
