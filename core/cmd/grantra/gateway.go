package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"

	"golang.org/x/crypto/ssh"

	"github.com/grantra-dev/grantra/core/internal/config"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
	"github.com/grantra-dev/grantra/core/internal/sshaccess/gateway"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// runGateway runs the SSH gateway people reach their servers through. It
// needs the database and the vault key (to sign its own certificates to the
// servers), and runs apart from the server so it can sit where SSH is
// allowed in.
func runGateway(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	keys, err := vault.KeysFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("vault key: %w", err)
	}
	if keys == nil {
		return errors.New("the SSH gateway needs the vault key the Grantra server uses (" + vault.KeyEnv + " or " + vault.ProviderEnv + ")")
	}
	hostKey, err := loadHostKey(cfg.Gateway.HostKeyFile, log)
	if err != nil {
		return err
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
	name, _ := os.Hostname()
	l, err := net.Listen("tcp", cfg.Gateway.Addr)
	if err != nil {
		return err
	}
	log.Info("SSH gateway listening", "addr", cfg.Gateway.Addr,
		"host_key", ssh.FingerprintSHA256(hostKey.PublicKey()))
	srv := &gateway.Server{Pool: pool, TenantID: tenantID, SSH: sshaccess.New(keys), HostKey: hostKey, Name: name, Log: log}
	return srv.Serve(ctx, l)
}

// loadHostKey reads the gateway's host key, creating it the first time.
func loadHostKey(path string, log *slog.Logger) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(priv, "grantra gateway")
		if err != nil {
			return nil, err
		}
		data = pem.EncodeToMemory(block)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, fmt.Errorf("save the gateway host key to %s (GRANTRA_GATEWAY_HOST_KEY_FILE): %w", path, err)
		}
		log.Warn("created a new SSH gateway host key: keep this file, or people see a host key warning", "path", path)
	} else if err != nil {
		return nil, fmt.Errorf("read the gateway host key: %w", err)
	}
	return ssh.ParsePrivateKey(data)
}
