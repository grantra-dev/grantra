//go:build pkcs11

package kms

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/miekg/pkcs11"
)

// PKCS11Supported reports whether this build talks to HSMs: it needs cgo
// and the pkcs11 build tag.
const PKCS11Supported = true

// PKCS11Key is an AES key in an HSM, used through its PKCS#11 module with
// AES-GCM; the associated data is GCM's additional data. It unwraps only
// with its own key: to move to another key, name the old one in
// GRANTRA_VAULT_OLD_KMS_KEY.
type PKCS11Key struct {
	ctx          *pkcs11.Ctx
	token, label string

	mu      sync.Mutex
	session pkcs11.SessionHandle
	key     pkcs11.ObjectHandle
}

// NewPKCS11 opens the token labelled token with the module (the vendor's
// .so file), logs in with pin and finds the AES key labelled label.
func NewPKCS11(module, token, pin, label string) (*PKCS11Key, error) {
	p := pkcs11.New(module)
	if p == nil {
		return nil, fmt.Errorf("cannot load PKCS#11 module %s", module)
	}
	// Another key from the same module may have initialized it already.
	if err := p.Initialize(); err != nil && !errors.Is(err, pkcs11.Error(pkcs11.CKR_CRYPTOKI_ALREADY_INITIALIZED)) {
		p.Destroy()
		return nil, fmt.Errorf("PKCS#11 initialize: %w", err)
	}
	slot, err := findSlot(p, token)
	if err != nil {
		p.Destroy()
		return nil, err
	}
	session, err := p.OpenSession(slot, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
	if err != nil {
		p.Destroy()
		return nil, fmt.Errorf("PKCS#11 open session: %w", err)
	}
	fail := func(err error) (*PKCS11Key, error) {
		p.CloseSession(session) //nolint:errcheck
		p.Destroy()
		return nil, err
	}
	if err := p.Login(session, pkcs11.CKU_USER, pin); err != nil && !errors.Is(err, pkcs11.Error(pkcs11.CKR_USER_ALREADY_LOGGED_IN)) {
		return fail(fmt.Errorf("PKCS#11 login to %s: %w", token, err))
	}
	k := &PKCS11Key{ctx: p, token: token, label: label, session: session}
	if k.key, err = k.find(label); err != nil {
		return fail(err)
	}
	return k, nil
}

func findSlot(p *pkcs11.Ctx, token string) (uint, error) {
	slots, err := p.GetSlotList(true)
	if err != nil {
		return 0, fmt.Errorf("PKCS#11 slots: %w", err)
	}
	for _, s := range slots {
		info, err := p.GetTokenInfo(s)
		if err == nil && info.Label == token {
			return s, nil
		}
	}
	return 0, fmt.Errorf("no PKCS#11 token labelled %q", token)
}

func (k *PKCS11Key) find(label string) (pkcs11.ObjectHandle, error) {
	tmpl := []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_SECRET_KEY),
		pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_AES),
		pkcs11.NewAttribute(pkcs11.CKA_LABEL, label),
	}
	if err := k.ctx.FindObjectsInit(k.session, tmpl); err != nil {
		return 0, err
	}
	objs, _, err := k.ctx.FindObjects(k.session, 2)
	k.ctx.FindObjectsFinal(k.session) //nolint:errcheck
	if err != nil {
		return 0, err
	}
	if len(objs) != 1 {
		return 0, fmt.Errorf("want one AES key labelled %q in the HSM, found %d", label, len(objs))
	}
	return objs[0], nil
}

// KeyID names the token too, so the same label in another HSM is another
// key and grantra vault rewrap moves data to it.
func (k *PKCS11Key) KeyID() string          { return PKCS11 + ":" + k.token + "/" + k.label }
func (k *PKCS11Key) Owns(keyID string) bool { return keyID == k.KeyID() }

const gcmIV = 12

// Wrap encrypts the data key with AES-GCM in the HSM; the stored value is
// the IV and the ciphertext with its tag.
func (k *PKCS11Key) Wrap(dataKey, aad []byte) ([]byte, error) {
	iv := make([]byte, gcmIV)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	params := pkcs11.NewGCMParams(iv, aad, 128)
	defer params.Free()
	if err := k.ctx.EncryptInit(k.session, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_GCM, params)}, k.key); err != nil {
		return nil, fmt.Errorf("PKCS#11 encrypt: %w", err)
	}
	ct, err := k.ctx.Encrypt(k.session, dataKey)
	if err != nil {
		return nil, fmt.Errorf("PKCS#11 encrypt: %w", err)
	}
	return append(iv, ct...), nil
}

func (k *PKCS11Key) Unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	if !k.Owns(keyID) {
		return nil, errNotOwned(keyID, k.KeyID())
	}
	if len(wrapped) <= gcmIV {
		return nil, errors.New("wrapped data key is too short")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	params := pkcs11.NewGCMParams(wrapped[:gcmIV], aad, 128)
	defer params.Free()
	if err := k.ctx.DecryptInit(k.session, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_GCM, params)}, k.key); err != nil {
		return nil, errUnwrap(PKCS11, err)
	}
	dk, err := k.ctx.Decrypt(k.session, wrapped[gcmIV:])
	if err != nil {
		return nil, errUnwrap(PKCS11, err)
	}
	return dk, nil
}
