package kms

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
)

// sealer is a fake key service's key: AES-GCM with the associated data.
type sealer struct{ aead cipher.AEAD }

func newSealer(t *testing.T) sealer {
	k := make([]byte, 32)
	rand.Read(k) //nolint:errcheck
	b, _ := aes.NewCipher(k)
	g, _ := cipher.NewGCM(b)
	return sealer{g}
}

func (s sealer) seal(pt, aad []byte) []byte {
	n := make([]byte, 12)
	rand.Read(n) //nolint:errcheck
	return s.aead.Seal(n, n, pt, aad)
}

func (s sealer) open(ct, aad []byte) ([]byte, bool) {
	if len(ct) < 12 {
		return nil, false
	}
	pt, err := s.aead.Open(nil, ct[:12], ct[12:], aad)
	return pt, err == nil
}

func unb64(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
}

// roundTrip checks what every provider must do: a wrapped key opens with its
// associated data, and not with another, nor once tampered with, nor under a
// key id of another kind.
func roundTrip(t *testing.T, p Provider) {
	t.Helper()
	dk := []byte("0123456789abcdef0123456789abcdef")
	aad := []byte("grantra-vault:v1:tenant:account:1")
	w, err := p.Wrap(dk, aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(w, dk) {
		t.Fatal("the wrapped key holds the data key")
	}
	got, err := p.Unwrap(p.KeyID(), w, aad)
	if err != nil || !bytes.Equal(got, dk) {
		t.Fatalf("unwrap = %q, %v", got, err)
	}
	if _, err := p.Unwrap(p.KeyID(), w, []byte("grantra-vault:v1:tenant:account:2")); err == nil {
		t.Error("unwrapped with other associated data")
	}
	bad := bytes.Clone(w)
	bad[len(bad)-1] ^= 1
	if _, err := p.Unwrap(p.KeyID(), bad, aad); err == nil {
		t.Error("unwrapped a tampered key")
	}
	if p.Owns("local:abc") || p.Owns(p.KeyID()+"x") || !p.Owns(p.KeyID()) {
		t.Errorf("owns: %s", p.KeyID())
	}
	if _, err := p.Unwrap("local:abc", w, aad); err == nil {
		t.Error("unwrapped under another kind of key id")
	}
}

func TestTransit(t *testing.T) {
	keys := map[string]sealer{"grantra": newSealer(t), "other": newSealer(t)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "s.token" || r.Header.Get("X-Vault-Namespace") != "team" {
			http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
			return
		}
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in) //nolint:errcheck
		op, name, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/kv-transit/"), "/")
		k, ok := keys[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		aad := unb64(in["associated_data"])
		switch op {
		case "keys":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"type": map[string]string{"grantra": "aes256-gcm96", "other": "rsa-2048"}[name]}}) //nolint:errcheck
		case "encrypt":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{ //nolint:errcheck
				"ciphertext": "vault:v1:" + base64.StdEncoding.EncodeToString(k.seal(unb64(in["plaintext"]), aad))}})
		case "decrypt":
			pt, ok := k.open(unb64(strings.TrimPrefix(in["ciphertext"], "vault:v1:")), aad)
			if !ok {
				http.Error(w, `{"errors":["cipher: message authentication failed"]}`, http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"plaintext": base64.StdEncoding.EncodeToString(pt)}}) //nolint:errcheck
		}
	}))
	defer srv.Close()
	conf := TransitConfig{Address: srv.URL + "/", Mount: "kv-transit", Key: "grantra", Token: "s.token", Namespace: "team"}
	p, err := NewTransit(context.Background(), conf)
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyID() != "transit:kv-transit/grantra" {
		t.Errorf("key id = %s", p.KeyID())
	}
	roundTrip(t, p)
	// A wrapped key of another mount is not sent to this one.
	if _, err := p.Unwrap("transit:elsewhere/grantra", []byte("vault:v1:x"), nil); err == nil {
		t.Error("unwrapped another mount's key")
	}
	// Keys that do not authenticate associated data are refused.
	conf.Key = "other"
	if _, err := NewTransit(context.Background(), conf); err == nil || !strings.Contains(err.Error(), "aes256-gcm96") {
		t.Errorf("rsa key = %v", err)
	}
	conf.Key, conf.Token = "grantra", "wrong"
	if _, err := NewTransit(context.Background(), conf); err == nil {
		t.Error("a wrong token passed")
	}
}

func TestGCP(t *testing.T) {
	const name = "projects/p/locations/europe-west1/keyRings/grantra/cryptoKeys/vault"
	k := newSealer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/")
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in) //nolint:errcheck
		aad := unb64(in["additionalAuthenticatedData"])
		switch {
		case r.Method == http.MethodGet && path == name:
			json.NewEncoder(w).Encode(map[string]any{"purpose": "ENCRYPT_DECRYPT", "primary": map[string]string{"state": "ENABLED"}}) //nolint:errcheck
		case path == name+":encrypt":
			json.NewEncoder(w).Encode(map[string]string{"ciphertext": base64.StdEncoding.EncodeToString(k.seal(unb64(in["plaintext"]), aad))}) //nolint:errcheck
		case path == name+":decrypt":
			pt, ok := k.open(unb64(in["ciphertext"]), aad)
			if !ok {
				http.Error(w, `{"error":{"message":"Decryption failed"}}`, http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"plaintext": base64.StdEncoding.EncodeToString(pt)}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p, err := newGCP(context.Background(), srv.Client(), srv.URL+"/v1/", name)
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyID() != "gcp-kms:"+name {
		t.Errorf("key id = %s", p.KeyID())
	}
	roundTrip(t, p)
	if _, err := newGCP(context.Background(), srv.Client(), srv.URL+"/v1/", "projects/p/../x"); err == nil {
		t.Error("accepted a malformed key name")
	}
	if _, err := p.Unwrap("gcp-kms:projects/p/locations/l/keyRings/r/cryptoKeys/x:decrypt?", nil, nil); err == nil {
		t.Error("accepted a malformed key id")
	}
}

func TestAWS(t *testing.T) {
	const arn = "arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	k := newSealer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKID/") {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		var in struct {
			KeyId             string
			Plaintext         []byte
			CiphertextBlob    []byte
			EncryptionContext map[string]string
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &in) //nolint:errcheck
		aad, _ := json.Marshal(in.EncryptionContext)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		switch r.Header.Get("X-Amz-Target") {
		case "TrentService.DescribeKey":
			json.NewEncoder(w).Encode(map[string]any{"KeyMetadata": map[string]any{ //nolint:errcheck
				"Arn": arn, "KeyId": "1234", "KeySpec": "SYMMETRIC_DEFAULT", "KeyUsage": "ENCRYPT_DECRYPT", "Enabled": true}})
		case "TrentService.Encrypt":
			json.NewEncoder(w).Encode(map[string]any{"CiphertextBlob": k.seal(in.Plaintext, aad), "KeyId": arn}) //nolint:errcheck
		case "TrentService.Decrypt":
			pt, ok := k.open(in.CiphertextBlob, aad)
			if !ok || in.KeyId != arn {
				w.Header().Set("X-Amzn-ErrorType", "InvalidCiphertextException")
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"__type":"InvalidCiphertextException"}`) //nolint:errcheck
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Plaintext": pt, "KeyId": arn}) //nolint:errcheck
		}
	}))
	defer srv.Close()
	client := awskms.New(awskms.Options{
		Region:       "eu-west-1",
		BaseEndpoint: aws.String(srv.URL),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret"}, nil
		}),
	})
	p, err := newAWS(context.Background(), client, "alias/grantra")
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyID() != "aws-kms:"+arn {
		t.Errorf("key id = %s", p.KeyID())
	}
	roundTrip(t, p)
	if _, err := p.Unwrap("aws-kms:arn:aws:kms:eu-west-1:111122223333:key/other", []byte("x"), nil); err == nil {
		t.Error("unwrapped under another key")
	}
}

func TestAzure(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	kid := func() string { return srv.URL + "/keys/grantra/0123456789abcdef0123456789abcdef" }
	ops := []string{"wrapKey", "unwrapKey", "sign", "verify"}
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("api-version") != "7.4" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in) //nolint:errcheck
		value, _ := base64.RawURLEncoding.DecodeString(in["value"])
		switch {
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"key": map[string]any{ //nolint:errcheck
				"kid": kid(), "kty": "RSA-HSM", "key_ops": ops,
				"n": base64.RawURLEncoding.EncodeToString(priv.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes())}})
		case strings.HasSuffix(r.URL.Path, "/sign") && in["alg"] == "PS256":
			sig, _ := rsa.SignPSS(rand.Reader, priv, crypto.SHA256, value, nil)
			json.NewEncoder(w).Encode(map[string]string{"kid": kid(), "value": base64.RawURLEncoding.EncodeToString(sig)}) //nolint:errcheck
		case strings.HasSuffix(r.URL.Path, "/unwrapkey") && in["alg"] == "RSA-OAEP-256":
			pt, err := rsa.DecryptOAEP(sha256.New(), nil, priv, value, nil)
			if err != nil {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"kid": kid(), "value": base64.RawURLEncoding.EncodeToString(pt)}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := azureKeyURL
	azureKeyURL = regexp.MustCompile(`^https://127\.0\.0\.1:[0-9]+/keys/[A-Za-z0-9-]+(/[0-9a-f]{32})?$`)
	defer func() { azureKeyURL = old }()
	token := func(context.Context) (string, error) { return "tok", nil }
	p, err := newAzure(context.Background(), srv.Client(), token, srv.URL+"/keys/grantra")
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyID() != "azure-keyvault:"+kid() {
		t.Errorf("key id = %s", p.KeyID())
	}
	roundTrip(t, p)
	// Other versions of the key are its own; keys of another vault or name
	// are not, so a key id in the database never sends the token elsewhere.
	other := strings.Repeat("a", 32)
	if !p.Owns("azure-keyvault:" + srv.URL + "/keys/grantra/" + other) {
		t.Error("does not own another version of its key")
	}
	for _, id := range []string{srv.URL + "/keys/other/" + other, "https://127.0.0.1:1/keys/grantra/" + other} {
		if p.Owns("azure-keyvault:" + id) {
			t.Errorf("owns %s", id)
		}
	}

	// A wrapped key someone made with the public key alone, without the
	// signature Key Vault makes, is refused.
	dk := []byte("0123456789abcdef0123456789abcdef")
	forged, _ := rsa.EncryptOAEP(sha256.New(), rand.Reader, &priv.PublicKey, dk, nil)
	blob := append([]byte{byte(len(forged) >> 8), byte(len(forged))}, forged...)
	blob = append(blob, make([]byte, 256)...)
	if _, err := p.Unwrap(p.KeyID(), blob, nil); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("forged wrapped key = %v", err)
	}
	ops = []string{"wrapKey", "unwrapKey"}
	if _, err := newAzure(context.Background(), srv.Client(), token, srv.URL+"/keys/grantra"); err == nil {
		t.Error("accepted a key that cannot sign")
	}
}
