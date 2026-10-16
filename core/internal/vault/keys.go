package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// KeyProvider wraps the data keys that seal each secret version. Grantra
// keeps only wrapped data keys, so whoever reads the database without the
// provider's key gets nothing. Providers backed by a cloud KMS, OpenBao/Vault
// Transit or an HSM implement the same interface (issue #84).
type KeyProvider interface {
	// KeyID names the key new data keys are wrapped with. It is stored with
	// every version so the right key unwraps it later.
	KeyID() string
	// Wrap encrypts a data key, binding it to aad.
	Wrap(dataKey, aad []byte) ([]byte, error)
	// Unwrap decrypts a data key wrapped by the key keyID with the same aad.
	Unwrap(keyID string, wrapped, aad []byte) ([]byte, error)
}

// KeyEnv is the environment variable holding the local master key.
const KeyEnv = "GRANTRA_VAULT_KEY"

// LocalKey is a master key held by the server, from GRANTRA_VAULT_KEY.
type LocalKey struct {
	id   string
	aead cipher.AEAD
}

// NewLocalKey builds a master key from 32 bytes in standard base64, such as
// the output of `openssl rand -base64 32`.
func NewLocalKey(encoded string) (*LocalKey, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s must be 32 random bytes in base64 (openssl rand -base64 32)", KeyEnv)
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(key)
	return &LocalKey{id: "local:" + hex.EncodeToString(sum[:8]), aead: aead}, nil
}

// LocalKeyFromEnv reads GRANTRA_VAULT_KEY. It returns nil without an error
// when the variable is not set: the vault is then off.
func LocalKeyFromEnv() (*LocalKey, error) {
	v := os.Getenv(KeyEnv)
	if v == "" {
		return nil, nil
	}
	return NewLocalKey(v)
}

// KeyID is "local:" and the start of the key's SHA-256, which tells keys
// apart without revealing them.
func (k *LocalKey) KeyID() string { return k.id }

// Wrap seals the data key with AES-256-GCM under the master key.
func (k *LocalKey) Wrap(dataKey, aad []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, dataKey, aad), nil
}

// Unwrap opens a data key wrapped by this master key.
func (k *LocalKey) Unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	if keyID != k.id {
		return nil, fmt.Errorf("secret was wrapped with key %s, but %s holds key %s", keyID, KeyEnv, k.id)
	}
	n := k.aead.NonceSize()
	if len(wrapped) < n {
		return nil, errors.New("wrapped data key is too short")
	}
	dk, err := k.aead.Open(nil, wrapped[:n], wrapped[n:], aad)
	if err != nil {
		return nil, errors.New("cannot unwrap data key: wrong master key or tampered data")
	}
	return dk, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealed is a secret value at rest: the ciphertext under a fresh data key,
// and that data key wrapped by the key provider.
type sealed struct {
	KeyID      string
	WrappedKey []byte
	Nonce      []byte
	Ciphertext []byte
}

// seal encrypts plaintext under a new data key. aad ties the result to the
// row it is stored in, so a ciphertext copied to another account or version
// does not open.
func seal(kp KeyProvider, plaintext, aad []byte) (sealed, error) {
	dataKey := make([]byte, 32)
	if _, err := rand.Read(dataKey); err != nil {
		return sealed{}, err
	}
	aead, err := newAEAD(dataKey)
	if err != nil {
		return sealed{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return sealed{}, err
	}
	wrapped, err := kp.Wrap(dataKey, aad)
	if err != nil {
		return sealed{}, fmt.Errorf("wrap data key: %w", err)
	}
	return sealed{KeyID: kp.KeyID(), WrappedKey: wrapped, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, aad)}, nil
}

// open decrypts what seal produced with the same aad.
func open(kp KeyProvider, s sealed, aad []byte) ([]byte, error) {
	dataKey, err := kp.Unwrap(s.KeyID, s.WrappedKey, aad)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(dataKey)
	if err != nil {
		return nil, err
	}
	out, err := aead.Open(nil, s.Nonce, s.Ciphertext, aad)
	if err != nil {
		return nil, errors.New("cannot decrypt secret: tampered data")
	}
	return out, nil
}

// versionAAD binds a sealed value to its tenant, account and version.
func versionAAD(tenantID, accountID string, version int) []byte {
	return fmt.Appendf(nil, "grantra-vault:v1:%s:%s:%d", tenantID, accountID, version)
}

// Sealed is a value sealed with a key provider, for other packages that keep
// secrets the same way the vault does, such as the SSH certificate authority.
type Sealed = sealed

// Seal encrypts plaintext under a new data key wrapped by kp, bound to aad.
func Seal(kp KeyProvider, plaintext, aad []byte) (Sealed, error) { return seal(kp, plaintext, aad) }

// Open decrypts what Seal produced with the same aad.
func Open(kp KeyProvider, s Sealed, aad []byte) ([]byte, error) { return open(kp, s, aad) }
