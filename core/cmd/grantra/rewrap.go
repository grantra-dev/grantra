package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/config"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/secretstore"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// rewrap wraps every data key again with the current vault key (grantra
// vault rewrap), after moving to a new key or provider, so the old key can
// be retired. Secrets are not decrypted, and it is safe to run again.
func rewrap(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	keys, err := vault.KeysFromEnv(ctx)
	if err != nil {
		return err
	}
	if keys == nil {
		return errors.New("no vault key is configured: set " + vault.KeyEnv + " or " + vault.ProviderEnv)
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	tenantID, err := db.EnsureTenant(ctx, pool, cfg.TenantSlug, cfg.TenantName)
	if err != nil {
		return err
	}
	var secrets, connectorSecrets, cas, gatewayCAs int
	err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) (err error) {
		if secrets, err = vault.Rewrap(ctx, tx, tenantID, keys); err != nil {
			return err
		}
		if connectorSecrets, err = secretstore.Rewrap(ctx, tx, tenantID, keys); err != nil {
			return err
		}
		if cas, err = sshaccess.Rewrap(ctx, tx, tenantID, keys); err != nil {
			return err
		}
		if gatewayCAs, err = gateways.Rewrap(ctx, tx, tenantID, keys); err != nil {
			return err
		}
		return db.Audit(ctx, tx, tenantID, "system:rewrap", "vault.rewrapped", "tenant", tenantID,
			map[string]any{"key": keys.KeyID(), "secret_versions": secrets, "connector_secrets": connectorSecrets, "ssh_cas": cas, "gateway_tls_cas": gatewayCAs})
	})
	if err != nil {
		return err
	}
	log.Info("re-wrapped every data key with the current vault key", "key", keys.KeyID(), "secret_versions", secrets, "connector_secrets", connectorSecrets, "ssh_cas", cas,
		"gateway_tls_cas", gatewayCAs)
	return nil
}
