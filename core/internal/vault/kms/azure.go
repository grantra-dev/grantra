package kms

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// AzureKey is an RSA key in Azure Key Vault or Managed HSM. Credentials come
// the usual Azure way: environment, workload identity or managed identity.
//
// RSA keys wrap with their public half, so anyone could wrap a data key of
// their own choosing. To keep a wrapped key from being planted, each one is
// also signed by the key itself, over the wrapped key and its associated
// data, and the signature is checked before unwrapping. The key therefore
// needs the wrapKey, unwrapKey and sign operations.
type AzureKey struct {
	hc    *http.Client
	token func(context.Context) (string, error)
	kid   string // current version: https://<vault>/keys/<name>/<version>

	mu   sync.Mutex
	pubs map[string]*rsa.PublicKey // by kid
}

var azureKeyURL = regexp.MustCompile(`^https://[a-z0-9-]+\.(vault\.azure\.net|managedhsm\.azure\.net|vault\.azure\.cn|vault\.usgovcloudapi\.net)/keys/[A-Za-z0-9-]+(/[0-9a-f]{32})?$`)

const azureAPI = "api-version=7.4"

// NewAzure uses the key at keyURL, https://<vault>.vault.azure.net/keys/<name>,
// in its current version.
func NewAzure(ctx context.Context, keyURL string) (*AzureKey, error) {
	if !azureKeyURL.MatchString(keyURL) {
		return nil, fmt.Errorf("Azure key must be https://<vault>.vault.azure.net/keys/<name>, not %q", keyURL)
	}
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("Azure credentials: %w", err)
	}
	scope := "https://vault.azure.net/.default"
	if u, _ := url.Parse(keyURL); strings.HasSuffix(u.Hostname(), ".managedhsm.azure.net") {
		scope = "https://managedhsm.azure.net/.default"
	}
	token := func(ctx context.Context) (string, error) {
		t, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{scope}})
		return t.Token, err
	}
	return newAzure(ctx, &http.Client{Timeout: timeout}, token, keyURL)
}

type azureJWK struct {
	Kid    string   `json:"kid"`
	Kty    string   `json:"kty"`
	KeyOps []string `json:"key_ops"`
	N      string   `json:"n"`
	E      string   `json:"e"`
}

func newAzure(ctx context.Context, hc *http.Client, token func(context.Context) (string, error), keyURL string) (*AzureKey, error) {
	k := &AzureKey{hc: hc, token: token, pubs: map[string]*rsa.PublicKey{}}
	jwk, pub, err := k.fetch(ctx, keyURL)
	if err != nil {
		return nil, fmt.Errorf("Azure key %s: %w", keyURL, err)
	}
	for _, op := range []string{"wrapKey", "unwrapKey", "sign"} {
		if !slices.Contains(jwk.KeyOps, op) {
			return nil, fmt.Errorf("Azure key %s must allow wrapKey, unwrapKey and sign; it allows %v", keyURL, jwk.KeyOps)
		}
	}
	if pub.N.BitLen() < 2048 {
		return nil, fmt.Errorf("Azure key %s must have at least 2048 bits", keyURL)
	}
	k.kid = jwk.Kid
	return k, nil
}

func (k *AzureKey) auth(r *http.Request) error {
	t, err := k.token(r.Context())
	if err != nil {
		return fmt.Errorf("Azure token: %w", err)
	}
	r.Header.Set("Authorization", "Bearer "+t)
	return nil
}

// fetch reads a key version's public half.
func (k *AzureKey) fetch(ctx context.Context, keyURL string) (azureJWK, *rsa.PublicKey, error) {
	var out struct {
		Key azureJWK `json:"key"`
	}
	if err := callJSON(ctx, k.hc, http.MethodGet, keyURL+"?"+azureAPI, k.auth, nil, &out); err != nil {
		return azureJWK{}, nil, err
	}
	jwk := out.Key
	if jwk.Kty != "RSA" && jwk.Kty != "RSA-HSM" {
		return jwk, nil, fmt.Errorf("must be an RSA key, not %s", jwk.Kty)
	}
	n, err1 := base64.RawURLEncoding.DecodeString(jwk.N)
	e, err2 := base64.RawURLEncoding.DecodeString(jwk.E)
	if err := errors.Join(err1, err2); err != nil || len(e) > 4 || !azureKeyURL.MatchString(jwk.Kid) {
		return jwk, nil, errors.New("the key service returned a malformed key")
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	k.mu.Lock()
	k.pubs[jwk.Kid] = pub
	k.mu.Unlock()
	return jwk, pub, nil
}

func (k *AzureKey) public(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	k.mu.Lock()
	pub := k.pubs[kid]
	k.mu.Unlock()
	if pub != nil {
		return pub, nil
	}
	_, pub, err := k.fetch(ctx, kid)
	return pub, err
}

func (k *AzureKey) KeyID() string { return Azure + ":" + k.kid }

// Owns accepts any version of the same key in the same vault.
func (k *AzureKey) Owns(keyID string) bool {
	kid, err := keyOf(Azure, keyID)
	return err == nil && azureKeyURL.MatchString(kid) && azureKeyBase(kid) == azureKeyBase(k.kid)
}

// azureKeyBase is a key URL without its version.
func azureKeyBase(kid string) string {
	if i := strings.Index(kid, "/keys/"); i >= 0 {
		if name, _, ok := strings.Cut(kid[i+len("/keys/"):], "/"); ok {
			return kid[:i] + "/keys/" + name
		}
	}
	return kid
}

// signed is what the signature covers: the wrapped key and its associated
// data, each with its length.
func signed(wrapped, aad []byte) []byte {
	h := sha256.New()
	for _, part := range [][]byte{[]byte("grantra-azure-wrap:v1"), wrapped, aad} {
		binary.Write(h, binary.BigEndian, uint32(len(part))) //nolint:errcheck
		h.Write(part)
	}
	return h.Sum(nil)
}

// Wrap encrypts the data key with the public key (RSA-OAEP-256) and has Key
// Vault sign the result. The stored value is the length of the wrapped key,
// the wrapped key and the signature.
func (k *AzureKey) Wrap(dataKey, aad []byte) ([]byte, error) {
	ctx, cancel := callCtx()
	defer cancel()
	pub, err := k.public(ctx, k.kid)
	if err != nil {
		return nil, err
	}
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, dataKey, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Value string `json:"value"`
	}
	in := map[string]string{"alg": "PS256", "value": base64.RawURLEncoding.EncodeToString(signed(wrapped, aad))}
	if err := callJSON(ctx, k.hc, http.MethodPost, k.kid+"/sign?"+azureAPI, k.auth, in, &out); err != nil {
		return nil, fmt.Errorf("Azure Key Vault sign: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(out.Value)
	if err != nil || len(sig) == 0 {
		return nil, errEmpty
	}
	blob := binary.BigEndian.AppendUint16(nil, uint16(len(wrapped)))
	return append(append(blob, wrapped...), sig...), nil
}

func (k *AzureKey) Unwrap(keyID string, blob, aad []byte) ([]byte, error) {
	if !k.Owns(keyID) {
		return nil, errNotOwned(keyID, k.KeyID())
	}
	kid, _ := keyOf(Azure, keyID)
	if len(blob) < 2 || len(blob) < 2+int(binary.BigEndian.Uint16(blob)) {
		return nil, errors.New("wrapped data key is too short")
	}
	n := int(binary.BigEndian.Uint16(blob))
	wrapped, sig := blob[2:2+n], blob[2+n:]
	ctx, cancel := callCtx()
	defer cancel()
	pub, err := k.public(ctx, kid)
	if err != nil {
		return nil, errUnwrap(Azure, err)
	}
	if err := rsa.VerifyPSS(pub, crypto.SHA256, signed(wrapped, aad), sig, nil); err != nil {
		return nil, errUnwrap(Azure, errors.New("the signature does not match: the wrapped key was not made by Grantra or belongs elsewhere"))
	}
	var out struct {
		Value string `json:"value"`
	}
	in := map[string]string{"alg": "RSA-OAEP-256", "value": base64.RawURLEncoding.EncodeToString(wrapped)}
	if err := callJSON(ctx, k.hc, http.MethodPost, kid+"/unwrapkey?"+azureAPI, k.auth, in, &out); err != nil {
		return nil, errUnwrap(Azure, err)
	}
	return base64.RawURLEncoding.DecodeString(out.Value)
}
