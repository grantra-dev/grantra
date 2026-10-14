// Package db owns the PostgreSQL connection pool and schema migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open creates a connection pool and verifies it can reach the database.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies every embedded migration that has not run yet, in file-name
// order, each inside its own transaction. A session-level advisory lock keeps
// concurrent replicas from migrating at the same time.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	const lockID = 7_244_617 // arbitrary, unique to Grantra migrations
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockID) //nolint:errcheck

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("apply %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", version, err)
		}
	}
	return nil
}

// GrantApp gives role the data permissions the server needs on every table
// and sequence in the schema, and nothing more: it does not own them, so it
// cannot alter a table or turn off its row-level security. Run it as the
// role that owns the tables, after Migrate.
func GrantApp(ctx context.Context, pool *pgxpool.Pool, role string) error {
	r := pgx.Identifier{role}.Sanitize()
	// Concurrent GRANTs on the same tables fail, so replicas migrating at
	// once (and the test helpers) queue on one lock. The statements run as
	// one implicit transaction, which holds it until they are done.
	_, err := pool.Exec(ctx, `SELECT pg_advisory_xact_lock(7244618);
		GRANT USAGE ON SCHEMA public TO `+r+`;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO `+r+`;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO `+r)
	if err != nil {
		return fmt.Errorf("grant data permissions to %s: %w", role, err)
	}
	return nil
}

// AppRoleProblems lists why the connected role is stronger than the server
// needs: it is a superuser, bypasses row-level security, or owns tables (an
// owner can turn row-level security off). Production installs connect as a
// role with none of these; see docs/production.md.
func AppRoleProblems(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var super, bypass bool
	var owned int
	err := pool.QueryRow(ctx, `SELECT r.rolsuper, r.rolbypassrls,
			(SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tableowner = current_user)
		FROM pg_roles r WHERE r.rolname = current_user`).Scan(&super, &bypass, &owned)
	if err != nil {
		return nil, err
	}
	var problems []string
	if super {
		problems = append(problems, "is a superuser")
	}
	if bypass {
		problems = append(problems, "bypasses row-level security")
	}
	if owned > 0 {
		problems = append(problems, fmt.Sprintf("owns %d tables", owned))
	}
	return problems, nil
}
