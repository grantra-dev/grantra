package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/ssh"

	"github.com/grantra-dev/grantra/core/internal/config"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/gateways/proxy"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
	"github.com/grantra-dev/grantra/core/internal/sshaccess/gateway"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// runGateway runs the gateway people reach their servers, clusters and
// databases through: SSH, and the Kubernetes, PostgreSQL and MySQL
// listeners. It needs the database and the vault key (to sign its own
// certificates and open the credentials it logs in with), and runs apart
// from the server so it can sit where those connections are allowed in.
func runGateway(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	keys, err := vault.KeysFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("vault key: %w", err)
	}
	if keys == nil {
		return errors.New("the gateway needs the vault key the Grantra server uses (" + vault.KeyEnv + " or " + vault.ProviderEnv + ")")
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

	gw := gateways.New(keys)
	tlsConf, err := gatewayTLS(ctx, log, cfg.Gateway, pool, tenantID, gw, name)
	if err != nil {
		return err
	}
	px := &proxy.Server{Pool: pool, TenantID: tenantID, Gateways: gw, TLS: tlsConf, Name: name, Log: log}
	listeners := []struct {
		what, addr string
		serve      func(context.Context, net.Listener) error
	}{
		{"Kubernetes", cfg.Gateway.KubernetesAddr, px.ServeKubernetes},
		{"PostgreSQL", cfg.Gateway.PostgresAddr, px.ServePostgres},
		{"MySQL", cfg.Gateway.MySQLAddr, px.ServeMySQL},
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	errs := make(chan error, len(listeners)+1)
	running := 1
	go func() { errs <- srv.Serve(ctx, l) }()
	for _, ln := range listeners {
		if ln.addr == config.Off {
			continue
		}
		pl, err := net.Listen("tcp", ln.addr)
		if err != nil {
			stop()
			for range running {
				<-errs
			}
			return fmt.Errorf("%s gateway: %w", ln.what, err)
		}
		log.Info(ln.what+" gateway listening", "addr", ln.addr)
		running++
		go func() {
			if err := ln.serve(ctx, pl); err != nil {
				errs <- fmt.Errorf("%s gateway: %w", ln.what, err)
				return
			}
			errs <- nil
		}()
	}
	// One listener failing stops them all.
	var first error
	for range running {
		if err := <-errs; err != nil && first == nil {
			first = err
			stop()
		}
	}
	return first
}

// gatewayTLS is the gateway's TLS certificate: the operator's own, or one
// of the tenant's gateway CA for cfg.TLSNames, renewed while it runs.
func gatewayTLS(ctx context.Context, log *slog.Logger, cfg config.Gateway, pool *pgxpool.Pool, tenantID string,
	gw *gateways.Service, host string) (*tls.Config, error) {
	conf := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.TLSCertFile != "" {
		return operatorTLS(log, conf, cfg.TLSCertFile, cfg.TLSKeyFile)
	}
	names := cfg.TLSNames
	if len(names) == 0 {
		names = []string{"localhost", "127.0.0.1"}
		if host != "" {
			names = append([]string{host}, names...)
		}
	}
	var (
		mu       sync.Mutex
		cert     *tls.Certificate
		renewing bool
		tried    time.Time
	)
	renew := func() error {
		var c *tls.Certificate
		err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) (err error) {
			c, err = gw.ServingCertificate(ctx, tx, tenantID, names)
			return err
		})
		if err != nil {
			return err
		}
		mu.Lock()
		cert = c
		mu.Unlock()
		return nil
	}
	if err := renew(); err != nil {
		return nil, fmt.Errorf("gateway TLS certificate: %w", err)
	}
	log.Info("gateway TLS certificate signed by the tenant's gateway CA", "names", names)
	conf.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		mu.Lock()
		defer mu.Unlock()
		// A third of its life before it ends, it is renewed (tried once a
		// minute at most); if renewing keeps failing it is still served
		// until it expires.
		if time.Until(cert.Leaf.NotAfter) < gateways.ServingCertDays*24*time.Hour/3 && !renewing && time.Since(tried) > time.Minute {
			renewing, tried = true, time.Now()
			go func() {
				err := renew()
				mu.Lock()
				renewing = false
				mu.Unlock()
				if err != nil {
					log.Error("could not renew the gateway TLS certificate", "err", err)
				}
			}()
		}
		return cert, nil
	}
	return conf, nil
}

// operatorTLS serves the operator's certificate, read again (once a minute
// at most) when its file changes, as cert-manager renews it.
func operatorTLS(log *slog.Logger, conf *tls.Config, certFile, keyFile string) (*tls.Config, error) {
	load := func() (*tls.Certificate, time.Time, error) {
		info, err := os.Stat(certFile)
		if err != nil {
			return nil, time.Time{}, err
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		return &cert, info.ModTime(), err
	}
	cert, modified, err := load()
	if err != nil {
		return nil, fmt.Errorf("gateway TLS certificate: %w", err)
	}
	var (
		mu      sync.Mutex
		checked = time.Now()
	)
	conf.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(checked) < time.Minute {
			return cert, nil
		}
		checked = time.Now()
		if info, err := os.Stat(certFile); err != nil || info.ModTime().Equal(modified) {
			return cert, nil
		}
		// A half-written pair fails to load: the old one serves until the
		// next check.
		if c, m, err := load(); err != nil {
			log.Error("could not read the renewed gateway TLS certificate", "err", err)
		} else {
			cert, modified = c, m
			log.Info("gateway TLS certificate reloaded")
		}
		return cert, nil
	}
	return conf, nil
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
