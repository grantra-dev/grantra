// Package kms wraps the vault's data keys with a key kept outside Grantra:
// a cloud KMS (AWS, Google Cloud, Azure Key Vault), OpenBao or HashiCorp
// Vault Transit, or an HSM through PKCS#11. Grantra then never holds the
// master key: whoever copies the database, or Grantra's configuration, gets
// nothing without access to that key, and every unwrap is in its audit log.
//
// Every provider binds a wrapped key to the associated data it was wrapped
// with (the tenant, account and version it belongs to), so a wrapped key
// copied to another row does not open, and none accepts a wrapped key
// someone made without the key itself.
package kms

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Provider is a key kept outside Grantra. It is the vault's KeyProvider, plus
// Owns, so several can share the work of unwrapping.
type Provider interface {
	// KeyID names the key new data keys are wrapped with: a prefix naming
	// the provider, then the key.
	KeyID() string
	Wrap(dataKey, aad []byte) ([]byte, error)
	// Unwrap opens a data key wrapped by keyID, which this provider owns.
	Unwrap(keyID string, wrapped, aad []byte) ([]byte, error)
	// Owns reports whether keyID is one this provider unwraps: its own key
	// (in any version, since keys rotate inside the provider) and no other,
	// so a key id written into the database cannot send Grantra, and its
	// credentials, to a key or a server someone else chose.
	Owns(keyID string) bool
}

// Kinds of provider, which start their key ids.
const (
	AWS     = "aws-kms"
	GCP     = "gcp-kms"
	Azure   = "azure-keyvault"
	Transit = "transit"
	PKCS11  = "pkcs11"
)

// timeout bounds one call to a key service.
const timeout = 30 * time.Second

func callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// keyOf returns the key part of a key id of kind.
func keyOf(kind, keyID string) (string, error) {
	key, ok := strings.CutPrefix(keyID, kind+":")
	if !ok || key == "" {
		return "", fmt.Errorf("key %s is not a %s key", keyID, kind)
	}
	return key, nil
}

func owns(kind, keyID string) bool { return strings.HasPrefix(keyID, kind+":") }

func errNotOwned(keyID, current string) error {
	return fmt.Errorf("data key was wrapped with %s, not with this Grantra's key %s", keyID, current)
}

// errUnwrap hides why a key service refused, which can say more than
// needed, behind one message; the cause is kept for the logs.
func errUnwrap(kind string, err error) error {
	return fmt.Errorf("cannot unwrap data key with %s: wrong key, no permission or tampered data: %w", kind, err)
}

var errEmpty = errors.New("the key service returned nothing")
