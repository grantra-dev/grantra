package telemetry

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/grantra-dev/grantra/core/internal/db"
)

// Metrics are Grantra's own metrics, on top of the HTTP and database ones the
// instrumentation libraries record. Every name starts with grantra.
type Metrics struct {
	// JobRuns counts background job passes by job and outcome (ok, error,
	// skipped when another replica held the job's lock).
	JobRuns metric.Int64Counter
	// JobDuration is how long each pass took, in seconds.
	JobDuration metric.Float64Histogram
	// ConnectorRuns counts aggregation and provisioning runs by operation and
	// status.
	ConnectorRuns metric.Int64Counter
	// ConnectorChanges adds up the counts each run reports (accounts read,
	// created, disabled...) by operation and count name.
	ConnectorChanges metric.Int64Counter
	// LifecycleEvents counts joiner, mover and leaver events turned into
	// birthright access changes; AccessChanges counts those changes.
	LifecycleEvents metric.Int64Counter
	AccessChanges   metric.Int64Counter
	// RequestsClosed counts access requests closed without a decision
	// (expired, rejected or cancelled), steps escalated because nobody
	// decided them in time, and access ended because its time was up.
	RequestsClosed metric.Int64Counter
	// Notifications counts emails by result (sent, retried, failed, skipped).
	Notifications metric.Int64Counter
}

// NewMetrics creates the instruments on the global meter provider: real ones
// after Setup with telemetry on, no-ops otherwise.
func NewMetrics() (*Metrics, error) {
	m := otel.Meter(Name)
	var errs [8]error
	var out Metrics
	out.JobRuns, errs[0] = m.Int64Counter("grantra.job.runs", metric.WithDescription("Background job passes by outcome (ok, error, skipped)."))
	out.JobDuration, errs[1] = m.Float64Histogram("grantra.job.duration", metric.WithUnit("s"), metric.WithDescription("Background job pass duration."))
	out.ConnectorRuns, errs[2] = m.Int64Counter("grantra.connector.runs", metric.WithDescription("Connector aggregation and provisioning runs."))
	out.ConnectorChanges, errs[3] = m.Int64Counter("grantra.connector.changes", metric.WithDescription("Counts reported by connector runs."))
	out.LifecycleEvents, errs[4] = m.Int64Counter("grantra.lifecycle.events", metric.WithDescription("Joiner, mover and leaver events processed."))
	out.AccessChanges, errs[5] = m.Int64Counter("grantra.access.changes", metric.WithDescription("Birthright access added or removed."))
	out.RequestsClosed, errs[6] = m.Int64Counter("grantra.access_requests.closed", metric.WithDescription("Access requests closed or escalated without a decision, and access ended at its end date."))
	out.Notifications, errs[7] = m.Int64Counter("grantra.notifications", metric.WithDescription("Notification emails by result."))
	return &out, errors.Join(errs[:]...)
}

// ObserveBacklog reports, at each metrics export, how much work is waiting:
// pending access requests, open separation of duties violations and queued
// notification emails. Only one replica needs it, but every replica reporting
// the same numbers is harmless.
func ObserveBacklog(pool *pgxpool.Pool, tenantID string) error {
	m := otel.Meter(Name)
	pending, err1 := m.Int64ObservableGauge("grantra.access_requests.pending", metric.WithDescription("Access requests waiting for a decision."))
	sod, err2 := m.Int64ObservableGauge("grantra.sod.violations", metric.WithDescription("Separation of duties violations by status (open, excepted)."))
	queued, err3 := m.Int64ObservableGauge("grantra.notifications.queued", metric.WithDescription("Notification emails waiting to be sent."))
	if err := errors.Join(err1, err2, err3); err != nil {
		return err
	}
	_, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		var p, open, excepted, q int64
		err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM access_requests WHERE status = 'pending'),
				(SELECT count(*) FROM sod_violations WHERE status = 'open'),
				(SELECT count(*) FROM sod_violations WHERE status = 'excepted'),
				(SELECT count(*) FROM notification_outbox WHERE status = 'pending')`).Scan(&p, &open, &excepted, &q)
		})
		if err != nil {
			return err
		}
		o.ObserveInt64(pending, p)
		o.ObserveInt64(sod, open, metric.WithAttributes(attribute.String("status", "open")))
		o.ObserveInt64(sod, excepted, metric.WithAttributes(attribute.String("status", "excepted")))
		o.ObserveInt64(queued, q)
		return nil
	}, pending, sod, queued)
	return err
}
