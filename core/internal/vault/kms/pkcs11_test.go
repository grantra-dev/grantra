//go:build pkcs11

package kms

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/miekg/pkcs11"
)

// softHSM finds SoftHSM's module, or skips when it is not installed. CI sets
// GRANTRA_TEST_PKCS11_MODULE so that the test cannot skip there.
func softHSM(t *testing.T) string {
	t.Helper()
	if m := os.Getenv("GRANTRA_TEST_PKCS11_MODULE"); m != "" {
		if _, err := os.Stat(m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	for _, m := range []string{"/usr/lib/softhsm/libsofthsm2.so", "/usr/lib/x86_64-linux-gnu/softhsm/libsofthsm2.so"} {
		if _, err := os.Stat(m); err == nil {
			return m
		}
	}
	t.Skip("SoftHSM is not installed")
	return ""
}

// newToken makes a SoftHSM token holding AES keys with the labels given.
func newToken(t *testing.T, module, pin string, labels ...string) {
	t.Helper()
	dir := t.TempDir()
	conf := filepath.Join(dir, "softhsm2.conf")
	if err := os.WriteFile(conf, []byte("directories.tokendir = "+dir+"\nobjectstore.backend = file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOFTHSM2_CONF", conf)
	if out, err := exec.Command("softhsm2-util", "--init-token", "--free", "--label", "grantra", "--so-pin", "0000", "--pin", pin).CombinedOutput(); err != nil {
		t.Fatalf("init token: %v: %s", err, out)
	}
	p := pkcs11.New(module)
	if err := p.Initialize(); err != nil {
		t.Fatal(err)
	}
	defer p.Destroy()
	defer p.Finalize() //nolint:errcheck
	slot, err := findSlot(p, "grantra")
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.OpenSession(slot, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
	if err != nil {
		t.Fatal(err)
	}
	defer p.CloseSession(s) //nolint:errcheck
	if err := p.Login(s, pkcs11.CKU_USER, pin); err != nil {
		t.Fatal(err)
	}
	for _, label := range labels {
		if _, err := p.GenerateKey(s, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_KEY_GEN, nil)}, []*pkcs11.Attribute{
			pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_SECRET_KEY),
			pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_AES),
			pkcs11.NewAttribute(pkcs11.CKA_VALUE_LEN, 32),
			pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true),
			pkcs11.NewAttribute(pkcs11.CKA_SENSITIVE, true),
			pkcs11.NewAttribute(pkcs11.CKA_EXTRACTABLE, false),
			pkcs11.NewAttribute(pkcs11.CKA_ENCRYPT, true),
			pkcs11.NewAttribute(pkcs11.CKA_DECRYPT, true),
			pkcs11.NewAttribute(pkcs11.CKA_LABEL, label),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPKCS11(t *testing.T) {
	module := softHSM(t)
	newToken(t, module, "1234", "grantra-old", "grantra-new")

	if _, err := NewPKCS11(module, "grantra", "wrong", "grantra-old"); err == nil {
		t.Fatal("wrong PIN accepted")
	}
	if _, err := NewPKCS11(module, "grantra", "1234", "missing"); err == nil {
		t.Fatal("missing key accepted")
	}
	old, err := NewPKCS11(module, "grantra", "1234", "grantra-old")
	if err != nil {
		t.Fatal(err)
	}
	if old.KeyID() != "pkcs11:grantra/grantra-old" {
		t.Fatalf("key id %s", old.KeyID())
	}
	roundTrip(t, old)

	// Another key of the same HSM opens only what it wrapped itself: moving
	// to it names the old one (GRANTRA_VAULT_OLD_KMS_KEY) until rewrap.
	dataKey := bytes.Repeat([]byte{7}, 32)
	wrapped, err := old.Wrap(dataKey, []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := NewPKCS11(module, "grantra", "1234", "grantra-new")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Owns(old.KeyID()) {
		t.Fatal("a key owns another key of its HSM")
	}
	if _, err := cur.Unwrap(old.KeyID(), wrapped, []byte("aad")); err == nil {
		t.Fatal("unwrapped with a key it was not configured for")
	}
	if _, err := cur.Unwrap(cur.KeyID(), wrapped, []byte("aad")); err == nil {
		t.Fatal("wrong key opened the data key")
	}
	if got, err := old.Unwrap(old.KeyID(), wrapped, []byte("aad")); err != nil || !bytes.Equal(got, dataKey) {
		t.Fatalf("old key, opened again from the same module: %v", err)
	}
}
