// Package apikey makes the secrets that programs use to call Grantra (app
// API keys, service tokens) and the hashes Grantra keeps instead of them.
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// New returns a random secret that starts with prefix, so leaked secrets
// are easy to spot, and the first characters people see to tell secrets
// apart.
func New(prefix string) (secret, shown string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	secret = prefix + base64.RawURLEncoding.EncodeToString(b)
	return secret, secret[:len(prefix)+6], nil
}

// Hash is what Grantra stores for a secret. Secrets are 256 random bits,
// so a plain SHA-256 is enough: there is nothing to guess.
func Hash(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}
