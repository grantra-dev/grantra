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

	startJobs(ctx, log, pool, tenantID)

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
		// Long enough for a 32 MiB HR file on a slow link; no WriteTimeout,
		// because a provisioning run asked from the console can take minutes.
		ReadTimeout: 5 * time.Minute,
		IdleTimeout: 2 * time.Minute,
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

// job is background work the server repeats on a timer. run does one pass
// and logs what it changed; an error it returns is logged for it.
type job struct {
	name  string
	every time.Duration
	run   func(ctx context.Context) error
}

// startJobs runs every background job once at start and then on its timer,
// until ctx ends.
func startJobs(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, tenantID string) {
	jobs := []job{
		// Start and end dates take effect between HR syncs too.
		{"apply start and end dates", 15 * time.Minute, func(ctx context.Context) error {
			activated, left, err := lifecycle.ApplyDueDates(ctx, pool, tenantID, time.Now())
			if activated+left > 0 {
				log.InfoContext(ctx, "applied start and end dates", "activated", activated, "left", left)
			}
			return err
		}},
		// Syncs from the console apply birthright access right away; this
		// catches due dates and anything a sync left behind.
		{"apply birthright access", 30 * time.Second, func(ctx context.Context) error {
			events, ch, err := access.ProcessLifecycle(ctx, pool, tenantID)
			if events > 0 {
				log.InfoContext(ctx, "applied birthright access", "events", events, "added", ch.Added, "removed", ch.Removed)
			}
			return err
		}},
		// Every application with a connector is read once an hour.
		{"aggregate applications", 5 * time.Minute, func(ctx context.Context) error {
			runs, err := accounts.AggregateDue(ctx, pool, tenantID, time.Hour, connector.EnvSecrets)
			for _, r := range runs {
				if r.Status == accounts.RunFailed {
					log.WarnContext(ctx, "aggregation failed", "application", r.ApplicationID, "error", r.Error)
				} else {
					log.InfoContext(ctx, "aggregated application", "application", r.ApplicationID, "counts", r.Counts)
				}
			}
			return err
		}},
		{"provision applications", 5 * time.Minute, func(ctx context.Context) error {
			runs, err := provisioning.ProvisionDue(ctx, pool, tenantID, connector.EnvSecrets)
			for _, r := range runs {
				if r.Status == accounts.RunSucceeded {
					log.InfoContext(ctx, "provisioned application", "application", r.ApplicationID, "counts", r.Counts)
				} else {
					log.WarnContext(ctx, "provisioning did not fully succeed", "application", r.ApplicationID, "status", r.Status, "error", r.Error, "counts", r.Counts)
				}
			}
			return err
		}},
		// Requests nobody decided in time, or for people who left, close;
		// access whose time is up ends.
		{"expire access requests", 5 * time.Minute, func(ctx context.Context) error {
			h, err := requests.ExpireDue(ctx, pool, tenantID)
			if h.Expired+h.Cancelled+h.AccessEnded > 0 {
				log.InfoContext(ctx, "closed access requests and ended expired access",
					"expired", h.Expired, "cancelled", h.Cancelled, "access_ended", h.AccessEnded)
			}
			return err
		}},
		{"close access reviews", 15 * time.Minute, func(ctx context.Context) error {
			n, err := reviews.CloseDue(ctx, pool, tenantID)
			if n > 0 {
				log.InfoContext(ctx, "closed access review campaigns", "campaigns", n)
			}
			return err
		}},
		{"scan separation of duties", 30 * time.Minute, func(ctx context.Context) error {
			res, err := sod.Scan(ctx, pool, tenantID)
			if res.Detected+res.Resolved+res.ExceptionsExpired > 0 {
				log.InfoContext(ctx, "separation of duties scan", "detected", res.Detected, "resolved", res.Resolved,
					"exceptions_expired", res.ExceptionsExpired, "open", res.Open)
			}
			return err
		}},
	}
	for _, j := range jobs {
		go func() {
			tick := time.NewTicker(j.every)
			defer tick.Stop()
			for {
				if err := j.run(ctx); err != nil && ctx.Err() == nil {
					log.ErrorContext(ctx, j.name, "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}()
	}
}
