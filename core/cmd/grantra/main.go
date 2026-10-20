// Command grantra runs the Grantra core server.
//
//	grantra          runs the server
//	grantra migrate  applies database migrations and exits
//	grantra gateway  runs the SSH gateway
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/auth"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/config"
	"github.com/grantra-dev/grantra/core/internal/connector"
	_ "github.com/grantra-dev/grantra/core/internal/connector/flatfile"     // registers the csv connector
	_ "github.com/grantra-dev/grantra/core/internal/connector/grantralogin" // registers the grantra-login connector
	_ "github.com/grantra-dev/grantra/core/internal/connector/ldap"         // registers the ldap connector
	_ "github.com/grantra-dev/grantra/core/internal/connector/scim"         // registers the scim connector
	"github.com/grantra-dev/grantra/core/internal/console"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/demo"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/gateways/cloud"
	"github.com/grantra-dev/grantra/core/internal/gateways/proxy"
	"github.com/grantra-dev/grantra/core/internal/itdr"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
	"github.com/grantra-dev/grantra/core/internal/login"
	"github.com/grantra-dev/grantra/core/internal/notify"
	"github.com/grantra-dev/grantra/core/internal/provisioning"
	"github.com/grantra-dev/grantra/core/internal/requests"
	"github.com/grantra-dev/grantra/core/internal/reviews"
	"github.com/grantra-dev/grantra/core/internal/server"
	"github.com/grantra-dev/grantra/core/internal/sod"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
	"github.com/grantra-dev/grantra/core/internal/telemetry"
	"github.com/grantra-dev/grantra/core/internal/vault"
	"github.com/grantra-dev/grantra/core/internal/webui"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tel, err := telemetry.Setup(ctx, server.Version)
	if err != nil {
		return fmt.Errorf("set up OpenTelemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tel.Shutdown(shutdownCtx) //nolint:errcheck
	}()
	var logs slog.Handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	if tel.LogHandler != nil {
		logs = slog.NewMultiHandler(logs, &minLevel{tel.LogHandler, level})
	}
	log := slog.New(logs)
	slog.SetDefault(log)
	if telemetry.Enabled() {
		log.Info("sending traces, metrics and logs through OpenTelemetry")
	}

	if len(os.Args) > 1 {
		switch {
		case os.Args[1] == "migrate" && len(os.Args) == 2:
			return migrate(ctx, log, cfg)
		case os.Args[1] == "gateway" && len(os.Args) == 2:
			return runGateway(ctx, log, cfg)
		case len(os.Args) == 3 && os.Args[1] == "vault" && os.Args[2] == "rewrap":
			return rewrap(ctx, log, cfg)
		}
		return fmt.Errorf("unknown command %q: run grantra, grantra migrate, grantra gateway or grantra vault rewrap", strings.Join(os.Args[1:], " "))
	}

	if cfg.MigrateOnStart {
		if err := migrate(ctx, log, cfg); err != nil {
			return err
		}
	} else {
		log.Info("not migrating on start: GRANTRA_MIGRATE_ON_START is false")
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if problems, err := db.AppRoleProblems(ctx, pool); err != nil {
		return err
	} else if len(problems) > 0 {
		log.Warn("the database role Grantra connects as is stronger than it needs; use a separate migration role in production (docs/production.md)",
			"problems", problems)
	}

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

	keys, err := vault.KeysFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("vault key: %w", err)
	}
	var (
		vlt    *vault.Vault
		sshSvc *sshaccess.Service
	)
	if keys != nil {
		vlt = vault.New(keys, connector.EnvSecrets)
		sshSvc = sshaccess.New(keys)
		log.Info("credential vault enabled", "key", keys.KeyID())
	} else {
		vlt = vault.New(nil, connector.EnvSecrets)
		sshSvc = sshaccess.New(nil)
		log.Warn("credential vault and SSH certificates are off: " + vault.KeyEnv + " is not set")
	}

	if cfg.Demo {
		seeded, err := demo.Seed(ctx, pool, tenantID, demo.Options{LoginURL: cfg.DemoLoginURL, Vault: vlt})
		if err != nil {
			return err
		}
		if seeded {
			log.Warn("loaded the demo company: GRANTRA_DEMO is for trying Grantra out, not for real data")
		}
	}

	sender := &notify.Sender{Pool: pool, TenantID: tenantID, From: cfg.SMTP.From, PublicURL: cfg.PublicURL, Log: log}
	if cfg.SMTP.Enabled() {
		sender.Mailer = notify.SMTP{
			Host:     cfg.SMTP.Host,
			Port:     cfg.SMTP.Port,
			Username: cfg.SMTP.Username,
			Password: cfg.SMTP.Password,
			TLS:      cfg.SMTP.TLS,
		}
		log.Info("notification email enabled", "smtp_host", cfg.SMTP.Host, "from", cfg.SMTP.From)
	} else {
		log.Warn("notification email disabled: GRANTRA_SMTP_HOST is not set")
	}

	metrics, err := telemetry.NewMetrics()
	if err != nil {
		return err
	}
	if err := telemetry.ObserveBacklog(pool, tenantID); err != nil {
		return err
	}

	gw := gateways.New(keys)
	consoles := &cloud.Consoles{Pool: pool, TenantID: tenantID, Gateways: gw, Issuer: cfg.PublicURL, Log: log}
	startJobs(ctx, log, pool, tenantID, sender, vlt, consoles, metrics)

	// Desktops run in this process, which the browser reaches: it relays
	// them to guacd.
	var desktops *proxy.Server
	if cfg.Gateway.Guacd != "" {
		name, _ := os.Hostname()
		desktops = &proxy.Server{Pool: pool, TenantID: tenantID, Gateways: gw, Name: name, Log: log,
			Guacd: cfg.Gateway.Guacd, MaxRecording: int64(cfg.Gateway.MaxRecordingMB) << 20}
		defer desktops.StopRDP()
		log.Info("desktops enabled", "guacd", cfg.Gateway.Guacd)
	}

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
		api.EmailEnabled = cfg.SMTP.Enabled()
		api.Vault = vlt
		api.SSH = sshSvc
		api.GatewayAddress = cfg.Gateway.PublicAddress
		api.Gateways = gw
		api.Consoles = consoles
		if desktops != nil {
			var origins []string
			if u, err := url.Parse(cfg.PublicURL); err == nil && u.Host != "" {
				origins = []string{u.Host}
			}
			api.Desktops = console.Desktops{Gateway: desktops, Context: ctx, Origins: origins}
		}
		api.GatewayAddresses = console.GatewayAddresses{Kubernetes: cfg.Gateway.KubernetesAddress,
			Postgres: cfg.Gateway.PostgresAddress, MySQL: cfg.Gateway.MySQLAddress, CAFile: cfg.Gateway.CAFile}
		log.Info("console login enabled", "issuer", cfg.OIDC.Issuer)
	} else {
		log.Warn("console login disabled: GRANTRA_OIDC_ISSUER is not set")
	}

	ui, ok := webui.Files()
	if !ok {
		log.Warn("web console not built into this binary: serving the placeholder home page (run make web)")
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           telemetry.HTTPHandler(server.New(log, pool, login, api, ui)),
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

// migrate applies the database migrations. With GRANTRA_MIGRATE_DATABASE_URL
// it connects as that role, which owns the tables, and grants the role in
// GRANTRA_DATABASE_URL data permissions only.
func migrate(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	url := cfg.MigrateDatabaseURL
	if url == "" {
		url = cfg.DatabaseURL
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	if cfg.MigrateDatabaseURL != "" {
		app, err := pgx.ParseConfig(cfg.DatabaseURL)
		if err != nil {
			return fmt.Errorf("GRANTRA_DATABASE_URL: %w", err)
		}
		if err := db.GrantApp(ctx, pool, app.User); err != nil {
			return err
		}
		log.Info("granted data permissions to the app role", "role", app.User)
	}
	log.Info("database migrated")
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
// until ctx ends. With several replicas, each pass runs on whichever replica
// takes the job's lock first; the others skip it.
func startJobs(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, tenantID string, sender *notify.Sender, vlt *vault.Vault,
	consoles *cloud.Consoles, m *telemetry.Metrics) {
	locks := db.NewJobLocks(pool)
	context.AfterFunc(ctx, func() { locks.Close(context.Background()) })

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
			m.LifecycleEvents.Add(ctx, int64(events))
			m.AccessChanges.Add(ctx, int64(ch.Added), metric.WithAttributes(attribute.String("change", "added")))
			m.AccessChanges.Add(ctx, int64(ch.Removed), metric.WithAttributes(attribute.String("change", "removed")))
			if events > 0 {
				log.InfoContext(ctx, "applied birthright access", "events", events, "added", ch.Added, "removed", ch.Removed)
			}
			return err
		}},
		// Every application with a connector is read once an hour.
		{"aggregate applications", 5 * time.Minute, func(ctx context.Context) error {
			runs, err := accounts.AggregateDue(ctx, pool, tenantID, time.Hour, connector.EnvSecrets)
			for _, r := range runs {
				m.RecordConnectorRun(ctx, r)
				if r.Status == accounts.RunFailed {
					log.WarnContext(ctx, "aggregation failed", "application", r.ApplicationID, "error", r.Error)
				} else {
					log.InfoContext(ctx, "aggregated application", "application", r.ApplicationID, "counts", r.Counts)
				}
			}
			return err
		}},
		// Under the "sensitive access" sign-in policy, Grantra Login asks
		// for a second factor from whoever holds high-risk access now.
		{"sync Grantra Login MFA", 5 * time.Minute, func(ctx context.Context) error {
			s, err := login.SyncMFA(ctx, pool, tenantID, connector.EnvSecrets)
			if s.Added+s.Removed > 0 {
				log.InfoContext(ctx, "synced Grantra Login MFA role", "added", s.Added, "removed", s.Removed)
			}
			return err
		}},
		{"provision applications", 5 * time.Minute, func(ctx context.Context) error {
			runs, err := provisioning.ProvisionDue(ctx, pool, tenantID, connector.EnvSecrets)
			for _, r := range runs {
				m.RecordConnectorRun(ctx, r.Run)
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
			m.RequestsClosed.Add(ctx, int64(h.Expired), metric.WithAttributes(attribute.String("outcome", "expired")))
			m.RequestsClosed.Add(ctx, int64(h.Cancelled), metric.WithAttributes(attribute.String("outcome", "cancelled")))
			m.RequestsClosed.Add(ctx, int64(h.AccessEnded), metric.WithAttributes(attribute.String("outcome", "access_ended")))
			if h.Expired+h.Cancelled+h.AccessEnded > 0 {
				log.InfoContext(ctx, "closed access requests and ended expired access",
					"expired", h.Expired, "cancelled", h.Cancelled, "access_ended", h.AccessEnded)
			}
			return err
		}},
		{"remind approvers", 15 * time.Minute, func(ctx context.Context) error {
			n, err := requests.RemindDue(ctx, pool, tenantID)
			if n > 0 {
				log.InfoContext(ctx, "reminded approvers of requests about to expire", "steps", n)
			}
			return err
		}},
		// Notifications are queued by the flows above and by the console;
		// a failed send waits and retries without holding them up.
		{"send notifications", 30 * time.Second, func(ctx context.Context) error {
			res, err := sender.SendDue(ctx)
			for result, n := range map[string]int{"sent": res.Sent, "retried": res.Retried, "failed": res.Failed, "skipped": res.Skipped} {
				m.Notifications.Add(ctx, int64(n), metric.WithAttributes(attribute.String("result", result)))
			}
			if res.Sent+res.Retried+res.Failed > 0 {
				log.InfoContext(ctx, "sent notifications", "sent", res.Sent, "retried", res.Retried,
					"failed", res.Failed, "skipped", res.Skipped)
			}
			return err
		}},
		{"remind reviewers", 15 * time.Minute, func(ctx context.Context) error {
			n, err := reviews.RemindDue(ctx, pool, tenantID)
			if n > 0 {
				log.InfoContext(ctx, "reminded reviewers of campaigns about to end", "campaigns", n)
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
		// Vault check-outs end on time, and passwords rotate when their
		// schedule says or after someone used them.
		{"rotate vault credentials", time.Minute, func(ctx context.Context) error {
			expired, rotations, err := vlt.RotateDue(ctx, pool, tenantID)
			if expired > 0 {
				log.InfoContext(ctx, "ended expired vault check-outs", "checkouts", expired)
			}
			for _, r := range rotations {
				if r.Rotated {
					log.InfoContext(ctx, "rotated vault password", "vault_account", r.VaultAccountID, "version", r.Version)
				} else {
					log.WarnContext(ctx, "vault password rotation failed", "vault_account", r.VaultAccountID, "error", r.Error)
				}
			}
			return err
		}},
		// Sessions whose gateway went away without saying so.
		{"end lost gateway sessions", time.Minute, func(ctx context.Context) error {
			var n, m int
			err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) (err error) {
				if n, err = sshaccess.EndLostSessions(ctx, tx, tenantID); err != nil {
					return err
				}
				m, err = gateways.EndLostSessions(ctx, tx, tenantID)
				return err
			})
			if n+m > 0 {
				log.WarnContext(ctx, "ended sessions their gateway stopped reporting on", "ssh_sessions", n, "database_and_cluster_sessions", m)
			}
			return err
		}},
		// Cloud console sessions end on time, and what they granted in the
		// cloud goes with them.
		{"end cloud console sessions", 30 * time.Second, func(ctx context.Context) error {
			n, err := consoles.EndSessions(ctx)
			if n > 0 {
				log.InfoContext(ctx, "ended cloud console sessions", "sessions", n)
			}
			return err
		}},
		{"remind break-glass reviews", 15 * time.Minute, func(ctx context.Context) error {
			n, err := vault.RemindDue(ctx, pool, tenantID)
			if n > 0 {
				log.InfoContext(ctx, "reminded admins of break-glass uses to review", "uses", n)
			}
			return err
		}},
		// Activations end on time, and what they changed reaches the
		// applications right away instead of at the next regular pass.
		{"apply just-in-time access", 30 * time.Second, func(ctx context.Context) error {
			var h access.JITHousekeeping
			err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) (err error) {
				h, err = access.EndActivations(ctx, tx, tenantID)
				return err
			})
			if err != nil {
				return err
			}
			if h.ActivationsEnded+h.EligibilitiesEnded > 0 {
				log.InfoContext(ctx, "ended just-in-time access", "activations", h.ActivationsEnded, "eligibilities", h.EligibilitiesEnded)
			}
			runs, err := provisioning.ProvisionQueued(ctx, pool, tenantID, connector.EnvSecrets)
			for _, r := range runs {
				m.RecordConnectorRun(ctx, r.Run)
				if r.Status == accounts.RunSucceeded {
					log.InfoContext(ctx, "provisioned just-in-time access", "application", r.ApplicationID, "counts", r.Counts)
				} else {
					log.WarnContext(ctx, "provisioning did not fully succeed", "application", r.ApplicationID, "status", r.Status, "error", r.Error, "counts", r.Counts)
				}
			}
			return err
		}},
		// Sign-ins are read from Grantra Login and looked at within a
		// minute, and so is access that appeared outside Grantra.
		{"detect identity threats", time.Minute, func(ctx context.Context) error {
			res, err := itdr.Scanner{Pool: pool, TenantID: tenantID, Secrets: connector.EnvSecrets}.Scan(ctx)
			if errors.Is(err, itdr.ErrScanRunning) {
				return nil // started from the console; it does this pass's work
			}
			if res.Raised+res.Escalated+res.Responses > 0 {
				log.InfoContext(ctx, "identity threat scan", "sign_ins_read", res.Read, "evaluated", res.Evaluated,
					"raised", res.Raised, "escalated", res.Escalated, "responses", res.Responses, "response_errors", res.ResponseErrors)
			}
			if res.ResponseErrors > 0 {
				log.WarnContext(ctx, "automatic threat responses did not fully succeed; see each alert", "failed", res.ResponseErrors)
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
	for i := range jobs {
		jobs[i].run = m.TraceJob(jobs[i].name, jobs[i].run)
	}
	for _, j := range jobs {
		go func() {
			tick := time.NewTicker(j.every)
			defer tick.Stop()
			for {
				ran, err := locks.TryRun(ctx, j.name+" "+tenantID, j.run)
				if err != nil && ctx.Err() == nil {
					log.ErrorContext(ctx, j.name, "err", err)
				} else if !ran && err == nil {
					m.JobSkipped(ctx, j.name)
					log.DebugContext(ctx, "skipped job: another replica is running it", "job", j.name)
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

// minLevel drops records below the configured log level before they reach
// the OpenTelemetry handler, which has no level of its own.
type minLevel struct {
	slog.Handler
	level slog.Level
}

func (h *minLevel) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level && h.Handler.Enabled(ctx, l)
}

func (h *minLevel) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &minLevel{h.Handler.WithAttrs(attrs), h.level}
}

func (h *minLevel) WithGroup(name string) slog.Handler {
	return &minLevel{h.Handler.WithGroup(name), h.level}
}
