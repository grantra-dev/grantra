// Command grantra runs the Grantra core server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grantra-dev/grantra/core/internal/auth"
	"github.com/grantra-dev/grantra/core/internal/config"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("grantra exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	log.Info("database migrated")

	tenantID, err := db.EnsureTenant(ctx, pool, cfg.TenantSlug, cfg.TenantName)
	if err != nil {
		return err
	}
	if cfg.BootstrapEmail != "" {
		created, err := db.BootstrapIdentity(ctx, pool, tenantID, cfg.BootstrapEmail)
		if err != nil {
			return err
		}
		if created {
			log.Info("created bootstrap identity", "email", cfg.BootstrapEmail)
		}
	}

	var login *auth.Service
	if cfg.OIDC.Enabled() {
		login = auth.New(auth.Config{
			Issuer:       cfg.OIDC.Issuer,
			DiscoveryURL: cfg.OIDC.DiscoveryURL,
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			PublicURL:    cfg.PublicURL,
			SessionIdle:  cfg.SessionIdle,
			SessionMax:   cfg.SessionMax,
		}, pool, tenantID, log)
		log.Info("console login enabled", "issuer", cfg.OIDC.Issuer)
	} else {
		log.Warn("console login disabled: GRANTRA_OIDC_ISSUER is not set")
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.New(log, pool, login),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr, "version", server.Version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}
