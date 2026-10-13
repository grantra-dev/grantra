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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/auth"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/config"
	"github.com/grantra-dev/grantra/core/internal/console"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
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
	if err := authz.EnsureBuiltinApp(ctx, pool, tenantID); err != nil {
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
		granted, err := authz.BootstrapAdmin(ctx, pool, tenantID, cfg.BootstrapEmail)
		if err != nil {
			return err
		}
		if granted {
			log.Warn("granted grantra-admin to the bootstrap identity because the tenant had no active admin", "email", cfg.BootstrapEmail)
		}
	}

	go applyDueDates(ctx, log, pool, tenantID)
	go applyBirthright(ctx, log, pool, tenantID)

	az, err := authz.New()
	if err != nil {
		return err
	}

	var (
		login *auth.Service
		api   *console.API
	)
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
		api = console.New(pool, tenantID, az, log)
		log.Info("console login enabled", "issuer", cfg.OIDC.Issuer)
	} else {
		log.Warn("console login disabled: GRANTRA_OIDC_ISSUER is not set")
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.New(log, pool, login, api),
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

// applyDueDates activates people whose start date has come and offboards
// people past their end date, on start and every quarter of an hour, so dates
// take effect between HR syncs too.
func applyDueDates(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, tenantID string) {
	tick := time.NewTicker(15 * time.Minute)
	defer tick.Stop()
	for {
		activated, left, err := lifecycle.ApplyDueDates(ctx, pool, tenantID, time.Now())
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "apply start and end dates", "err", err)
		case activated+left > 0:
			log.InfoContext(ctx, "applied start and end dates", "activated", activated, "left", left)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// applyBirthright gives and takes birthright access for every lifecycle event
// not yet processed, every 30 seconds. Syncs from the console also do it right
// away; this catches due dates and anything a sync left behind.
func applyBirthright(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, tenantID string) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		events, ch, err := access.ProcessLifecycle(ctx, pool, tenantID)
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "apply birthright access", "err", err)
		case events > 0:
			log.InfoContext(ctx, "applied birthright access", "events", events, "added", ch.Added, "removed", ch.Removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
