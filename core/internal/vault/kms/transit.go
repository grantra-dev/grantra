package kms

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// TransitConfig says how to reach an OpenBao or HashiCorp Vault Transit key.
type TransitConfig struct {
	// Address is the server, such as https://openbao.example.com:8200.
	Address string
	// Mount is where the transit engine is mounted (default "transit").
	Mount string
	// Key is the transit key's name.
	Key string
	// Token, or TokenFile: a file read on every call, such as the one an
	// agent sidecar keeps renewed.
	Token     string
	TokenFile string
	// Namespace is the Vault Enterprise or OpenBao namespace, if any.
	Namespace string
	// CACert is a PEM file to trust for the server's certificate.
	CACert string
}

// TransitKey is an OpenBao or HashiCorp Vault Transit key of an AEAD type
// (aes256-gcm96, aes128-gcm96 or chacha20-poly1305). The associated data
// goes in as associated_data, which those types authenticate.
type TransitKey struct {
	hc   *http.Client
	conf TransitConfig
}

var transitName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// NewTransit checks the key and returns it.
func NewTransit(ctx context.Context, conf TransitConfig) (*TransitKey, error) {
	if conf.Mount == "" {
		conf.Mount = "transit"
	}
	conf.Address = strings.TrimRight(conf.Address, "/")
	if u, err := url.Parse(conf.Address); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("transit address %q must be an http(s) URL", conf.Address)
	}
	if !transitName.MatchString(conf.Key) {
		return nil, fmt.Errorf("transit key name %q may use letters, digits, '.', '-' and '_'", conf.Key)
	}
	if conf.Token == "" && conf.TokenFile == "" {
		return nil, errors.New("transit needs a token or a token file")
	}
	hc := &http.Client{Timeout: timeout}
	if conf.CACert != "" {
		pem, err := os.ReadFile(conf.CACert)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no PEM certificate", conf.CACert)
		}
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	k := &TransitKey{hc: hc, conf: conf}
	var key struct {
		Data struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := k.call(ctx, http.MethodGet, "keys/"+conf.Key, nil, &key); err != nil {
		return nil, fmt.Errorf("transit key %s: %w", conf.Key, err)
	}
	switch key.Data.Type {
	case "aes256-gcm96", "aes128-gcm96", "chacha20-poly1305":
	default:
		return nil, fmt.Errorf("transit key %s must be aes256-gcm96, aes128-gcm96 or chacha20-poly1305, not %s", conf.Key, key.Data.Type)
	}
	// A server that predates associated_data accepts it with only a warning
	// and ignores it, which would leave data keys unbound: check it binds.
	probe, err := k.Wrap(make([]byte, 32), []byte("grantra-transit-check:a"))
	if err != nil {
		return nil, err
	}
	if _, err := k.Unwrap(k.KeyID(), probe, []byte("grantra-transit-check:b")); err == nil {
		return nil, fmt.Errorf("transit at %s ignores associated_data, which Grantra needs: use OpenBao or a newer HashiCorp Vault", conf.Address)
	}
	return k, nil
}

func (k *TransitKey) call(ctx context.Context, method, path string, in, out any) error {
	return callJSON(ctx, k.hc, method, k.conf.Address+"/v1/"+k.conf.Mount+"/"+path, func(r *http.Request) error {
		token := k.conf.Token
		if k.conf.TokenFile != "" {
			b, err := os.ReadFile(k.conf.TokenFile)
			if err != nil {
				return err
			}
			token = strings.TrimSpace(string(b))
		}
		r.Header.Set("X-Vault-Token", token)
		if k.conf.Namespace != "" {
			r.Header.Set("X-Vault-Namespace", k.conf.Namespace)
		}
		return nil
	}, in, out)
}

func (k *TransitKey) KeyID() string          { return Transit + ":" + k.conf.Mount + "/" + k.conf.Key }
func (k *TransitKey) Owns(keyID string) bool { return keyID == k.KeyID() }

func (k *TransitKey) Wrap(dataKey, aad []byte) ([]byte, error) {
	ctx, cancel := callCtx()
	defer cancel()
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	in := map[string]string{"plaintext": b64(dataKey), "associated_data": b64(aad)}
	if err := k.call(ctx, http.MethodPost, "encrypt/"+k.conf.Key, in, &out); err != nil {
		return nil, fmt.Errorf("transit encrypt: %w", err)
	}
	if !strings.HasPrefix(out.Data.Ciphertext, "vault:") {
		return nil, errEmpty
	}
	return []byte(out.Data.Ciphertext), nil
}

func (k *TransitKey) Unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	if !k.Owns(keyID) {
		return nil, errNotOwned(keyID, k.KeyID())
	}
	name := k.conf.Key
	ctx, cancel := callCtx()
	defer cancel()
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	in := map[string]string{"ciphertext": string(wrapped), "associated_data": b64(aad)}
	if err := k.call(ctx, http.MethodPost, "decrypt/"+name, in, &out); err != nil {
		return nil, errUnwrap(Transit, err)
	}
	return base64.StdEncoding.DecodeString(out.Data.Plaintext)
}
