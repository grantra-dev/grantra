package db

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JobLocks makes sure a background job runs on one replica at a time. Each
// job takes a PostgreSQL session advisory lock named after it before a pass;
// a replica that cannot take the lock skips that pass.
//
// The locks live on one connection of their own, outside the pool, so a job
// holding its lock never keeps a pooled connection busy while it works. If
// that connection drops, PostgreSQL releases its locks; the next pass
// reconnects.
type JobLocks struct {
	cfg *pgx.ConnConfig

	mu   sync.Mutex
	conn *pgx.Conn
	// gen counts connections, so an unlock never goes to a newer connection
	// than the one that took the lock.
	gen int
}

// NewJobLocks prepares job locks on the same database and role as pool. It
// connects on first use.
func NewJobLocks(pool *pgxpool.Pool) *JobLocks {
	return &JobLocks{cfg: pool.Config().ConnConfig.Copy()}
}

// TryRun runs fn if no other replica holds the lock for name, and reports
// whether it ran. The error is fn's, or the reason the lock could not be
// checked.
func (l *JobLocks) TryRun(ctx context.Context, name string, fn func(ctx context.Context) error) (bool, error) {
	gen, ok, err := l.lock(ctx, name)
	if err != nil || !ok {
		return false, err
	}
	defer l.unlock(context.WithoutCancel(ctx), name, gen)
	return true, fn(ctx)
}

func (l *JobLocks) lock(ctx context.Context, name string) (gen int, ok bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil || l.conn.IsClosed() {
		conn, err := pgx.ConnectConfig(ctx, l.cfg)
		if err != nil {
			return 0, false, fmt.Errorf("connect for job locks: %w", err)
		}
		l.conn = conn
		l.gen++
	}
	if err := l.conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, jobLockKey(name)).Scan(&ok); err != nil {
		l.conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck
		l.conn = nil
		return 0, false, fmt.Errorf("take job lock: %w", err)
	}
	return l.gen, ok, nil
}

func (l *JobLocks) unlock(ctx context.Context, name string, gen int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil || l.gen != gen {
		return // the connection that held the lock is gone, and the lock with it
	}
	if _, err := l.conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, jobLockKey(name)); err != nil {
		l.conn.Close(ctx) //nolint:errcheck
		l.conn = nil
	}
}

// Close drops the lock connection, releasing any locks still held.
func (l *JobLocks) Close(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil {
		l.conn.Close(ctx) //nolint:errcheck
		l.conn = nil
	}
}

// jobLockKey keeps job locks apart from other advisory locks, such as the
// migration lock.
func jobLockKey(name string) string { return "grantra job: " + name }
