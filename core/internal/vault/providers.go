package vault

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/vault/kms"
)

// Environment variables choosing where the master key lives.
const (
	// ProviderEnv is local (GRANTRA_VAULT_KEY, the default), aws-kms,
	// gcp-kms, azure-keyvault, transit or pkcs11.
	ProviderEnv = "GRANTRA_VAULT_KEY_PROVIDER"
	// KMSKeyEnv names the key in the provider: an AWS key ARN or alias, a
	// Google Cloud KMS key name, an Azure key URL, a transit key name or an
	// HSM key label.
	KMSKeyEnv = "GRANTRA_VAULT_KMS_KEY"
	// OldProviderEnv and OldKMSKeyEnv name a key that only unwraps, while
	// moving to another one.
	OldProviderEnv = "GRANTRA_VAULT_OLD_KEY_PROVIDER"
	OldKMSKeyEnv   = "GRANTRA_VAULT_OLD_KMS_KEY"
)

// Owns reports whether keyID is this key.
func (k *LocalKey) Owns(keyID string) bool { return keyID == k.id }

// keyring wraps with one key and unwraps with whichever of its keys wrapped
// the data key, so a deployment can move to another provider (keeping
// GRANTRA_VAULT_KEY for what it wrapped until grantra vault rewrap is done).
type keyring struct {
	current kms.Provider
	others  []kms.Provider
}

func (r *keyring) KeyID() string                            { return r.current.KeyID() }
func (r *keyring) Wrap(dataKey, aad []byte) ([]byte, error) { return r.current.Wrap(dataKey, aad) }

func (r *keyring) Unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	for _, p := range append([]kms.Provider{r.current}, r.others...) {
		if p.Owns(keyID) {
			return p.Unwrap(keyID, wrapped, aad)
		}
	}
	return nil, fmt.Errorf("secret was wrapped with key %s, which this Grantra is not configured to use (%s=%s)", keyID, ProviderEnv, r.current.KeyID())
}

// KeysFromEnv returns the key provider the environment configures, or nil
// when there is none: the vault and SSH certificates are then off. With an
// external provider, GRANTRA_VAULT_KEY, if still set, only unwraps what it
// wrapped before, and so does the provider GRANTRA_VAULT_OLD_KEY_PROVIDER and
// GRANTRA_VAULT_OLD_KMS_KEY name: what a move from one to the other needs
// until grantra vault rewrap has run.
func KeysFromEnv(ctx context.Context) (KeyProvider, error) {
	local, err := LocalKeyFromEnv()
	if err != nil {
		return nil, err
	}
	provider := strings.TrimSpace(os.Getenv(ProviderEnv))
	if provider == "" || provider == "local" {
		if local == nil {
			if provider == "local" {
				return nil, fmt.Errorf("%s=local needs %s", ProviderEnv, KeyEnv)
			}
			return nil, nil
		}
		return local, nil
	}
	r := &keyring{}
	if old := strings.TrimSpace(os.Getenv(OldProviderEnv)); old != "" {
		o, err := providerFromEnv(ctx, OldProviderEnv, old, OldKMSKeyEnv)
		if err != nil {
			return nil, fmt.Errorf("old vault key: %w", err)
		}
		r.others = append(r.others, o)
	}
	if r.current, err = providerFromEnv(ctx, ProviderEnv, provider, KMSKeyEnv); err != nil {
		return nil, err
	}
	if local != nil {
		r.others = append(r.others, local)
	}
	return r, nil
}

// providerFromEnv opens the key keyEnv names in provider, which providerEnv
// set; the connection settings are shared by both providers of a kind.
func providerFromEnv(ctx context.Context, providerEnv, provider, keyEnv string) (kms.Provider, error) {
	switch provider {
	case kms.AWS, kms.GCP, kms.Azure, kms.Transit, kms.PKCS11:
	default:
		return nil, fmt.Errorf("%s must be local, aws-kms, gcp-kms, azure-keyvault, transit or pkcs11, not %q", providerEnv, provider)
	}
	key := strings.TrimSpace(os.Getenv(keyEnv))
	if key == "" {
		return nil, fmt.Errorf("%s=%s needs %s, the key to use", providerEnv, provider, keyEnv)
	}
	switch provider {
	case kms.AWS:
		return kms.NewAWS(ctx, key)
	case kms.GCP:
		return kms.NewGCP(ctx, key)
	case kms.Azure:
		return kms.NewAzure(ctx, key)
	case kms.Transit:
		return kms.NewTransit(ctx, kms.TransitConfig{
			Address:   firstEnv("GRANTRA_VAULT_TRANSIT_ADDR", "VAULT_ADDR", "BAO_ADDR"),
			Mount:     os.Getenv("GRANTRA_VAULT_TRANSIT_MOUNT"),
			Key:       key,
			Token:     firstEnv("GRANTRA_VAULT_TRANSIT_TOKEN", "VAULT_TOKEN", "BAO_TOKEN"),
			TokenFile: os.Getenv("GRANTRA_VAULT_TRANSIT_TOKEN_FILE"),
			Namespace: firstEnv("GRANTRA_VAULT_TRANSIT_NAMESPACE", "VAULT_NAMESPACE", "BAO_NAMESPACE"),
			CACert:    firstEnv("GRANTRA_VAULT_TRANSIT_CACERT", "VAULT_CACERT", "BAO_CACERT"),
		})
	default:
		pin := os.Getenv("GRANTRA_VAULT_PKCS11_PIN")
		if file := os.Getenv("GRANTRA_VAULT_PKCS11_PIN_FILE"); file != "" {
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, err
			}
			pin = strings.TrimSpace(string(b))
		}
		return kms.NewPKCS11(os.Getenv("GRANTRA_VAULT_PKCS11_MODULE"), os.Getenv("GRANTRA_VAULT_PKCS11_TOKEN"), pin, key)
	}
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// Rewrap re-wraps the data key of every secret version that the current key
// did not wrap, so an old key can be retired. The secrets themselves are not
// decrypted. It returns how many it re-wrapped.
func Rewrap(ctx context.Context, tx pgx.Tx, tenantID string, kp KeyProvider) (int, error) {
	rows, err := tx.Query(ctx,
		`SELECT vault_account_id, version, key_id, wrapped_key FROM vault_secret_versions WHERE key_id <> $1 FOR UPDATE`, kp.KeyID())
	if err != nil {
		return 0, err
	}
	type version struct {
		account string
		version int
		keyID   string
		wrapped []byte
	}
	todo, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (version, error) {
		var v version
		return v, row.Scan(&v.account, &v.version, &v.keyID, &v.wrapped)
	})
	if err != nil {
		return 0, err
	}
	for _, v := range todo {
		wrapped, err := RewrapKey(kp, v.keyID, v.wrapped, versionAAD(tenantID, v.account, v.version))
		if err != nil {
			return 0, fmt.Errorf("vault account %s version %d: %w", v.account, v.version, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE vault_secret_versions SET key_id = $3, wrapped_key = $4 WHERE vault_account_id = $1 AND version = $2`,
			v.account, v.version, kp.KeyID(), wrapped); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

// RewrapKey unwraps a data key and wraps it again with kp's current key.
func RewrapKey(kp KeyProvider, keyID string, wrapped, aad []byte) ([]byte, error) {
	dataKey, err := kp.Unwrap(keyID, wrapped, aad)
	if err != nil {
		return nil, err
	}
	defer clear(dataKey)
	return kp.Wrap(dataKey, aad)
}
