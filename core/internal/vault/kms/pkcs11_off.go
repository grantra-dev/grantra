//go:build !pkcs11

package kms

import "errors"

// PKCS11Supported reports whether this build talks to HSMs: it needs cgo
// and the pkcs11 build tag, which the release binaries and image leave out.
const PKCS11Supported = false

// PKCS11Key is not available in this build.
type PKCS11Key struct{ Provider }

// NewPKCS11 says how to get a build that supports HSMs.
func NewPKCS11(module, token, pin, label string) (*PKCS11Key, error) {
	return nil, errors.New("this Grantra build cannot use PKCS#11 HSMs: build it with CGO_ENABLED=1 go build -tags pkcs11 (see docs/vault.md#key-providers)")
}
