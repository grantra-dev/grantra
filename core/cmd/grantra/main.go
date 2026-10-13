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
	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/auth"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/config"
	"github.com/grantra-dev/grantra/core/internal/connector"
	_ "github.com/grantra-dev/grantra/core/internal/connector/scim" // registers the scim connector
	"github.com/grantra-dev/grantra/core/internal/console"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
	"github.com/grantra-dev/grantra/core/internal/provisioning"
	"github.com/grantra-dev/grantra/core/internal/requests"
	"github.com/grantra-dev/grantra/core/internal/reviews"
	"github.com/grantra-dev/grantra/core/internal/server"
	"github.com/grantra-dev/grantra/core/internal/sod"
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
	go aggregateApplications(ctx, log, pool, tenantID)
	go provisionApplications(ctx, log, pool, tenantID)
	go expireRequests(ctx, log, pool, tenantID)
	go closeReviews(ctx, log, pool, tenantID)
	go scanSoD(ctx, log, pool, tenantID)

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

// aggregateApplications reads every application with a connector once an
// hour, checking every five minutes which ones are due.
func aggregateApplications(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, tenantID string) {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		runs, err := accounts.AggregateDue(ctx, pool, tenantID, time.Hour, connector.EnvSecrets)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "aggregate applications", "err", err)
		}
		for _, r := range runs {
			if r.Status == accounts.RunFailed {
				log.WarnContext(ctx, "aggregation failed", "application", r.ApplicationID, "error", r.Error)
			} else {
				log.InfoContext(ctx, "aggregated application", "application", r.ApplicationID, "counts", r.Counts)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// provisionApplications writes pending changes to every application with
// provisioning on, every five minutes.
func provisionApplications(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, tenantID string) {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		runs, err := provisioning.ProvisionDue(ctx, pool, tenantID, connector.EnvSecrets)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "provision applications", "err", err)
		}
		for _, r := range runs {
			if r.Status == accounts.RunSucceeded {
				log.InfoContext(ctx, "provisioned application", "application", r.ApplicationID, "counts", r.Counts)
			} else {
				log.WarnContext(ctx, "provisioning did not fully succeed", "application", r.ApplicationID, "status", r.Status, "error", r.Error, "counts", r.Counts)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// expireRequests closes access requests nobody decided in time, or for people
// who left, and removes access whose time is up, every five minutes.
func expireRequests(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, tenantID string) {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		h, err := requests.ExpireDue(ctx, pool, tenantID)
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "expire access requests", "err", err)
		case h.Expired+h.Cancelled+h.AccessEnded > 0:
			log.InfoContext(ctx, "closed access requests and ended expired access",
				"expired", h.Expired, "cancelled", h.Cancelled, "access_ended", h.AccessEnded)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// closeReviews ends access review campaigns past their due date, every
// fifteen minutes.
func closeReviews(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, tenantID string) {
	tick := time.NewTicker(15 * time.Minute)
	defer tick.Stop()
	for {
		n, err := reviews.CloseDue(ctx, pool, tenantID)
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "close access reviews", "err", err)
		case n > 0:
			log.InfoContext(ctx, "closed access review campaigns", "campaigns", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// scanSoD looks for separation of duties violations every half hour.
func scanSoD(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, tenantID string) {
	tick := time.NewTicker(30 * time.Minute)
	defer tick.Stop()
	for {
		res, err := sod.Scan(ctx, pool, tenantID)
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "scan separation of duties", "err", err)
		case res.Detected+res.Resolved+res.ExceptionsExpired > 0:
			log.InfoContext(ctx, "separation of duties scan", "detected", res.Detected, "resolved", res.Resolved,
				"exceptions_expired", res.ExceptionsExpired, "open", res.Open)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
