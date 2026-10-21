package lifecycle

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Feed reads the people of an HR system Grantra connects to itself, such as
// Workday or BambooHR. It answers rows of named values, one per person, as a
// file export would have them: Grantra reads them like the lines of a file,
// with the source's column mapping, so every check and rule of files holds.
type Feed interface {
	// Rows returns every person, or an error: a partial list would look
	// like leavers.
	Rows(ctx context.Context) ([]map[string]string, error)
}

// Text is a value from an HR system's JSON as a file would hold it: text
// trimmed, numbers as written (1000000, not 1e+06), true or false. Nulls,
// lists and objects have no such text, and ok is false.
func Text(v any) (text string, ok bool) {
	switch v := v.(type) {
	case string:
		return strings.TrimSpace(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}

// FeedFactory builds a Feed from a source's connection settings. Like a
// connector Factory it must fail on unknown settings and resolve secrets
// through secrets.
type FeedFactory func(connection json.RawMessage, secrets connector.SecretResolver) (Feed, error)

var (
	feedsMu sync.RWMutex
	feeds   = map[string]FeedFactory{}
)

// RegisterFeed makes an HR system available as a source kind. It panics on
// a duplicate name.
func RegisterFeed(kind string, f FeedFactory) {
	feedsMu.Lock()
	defer feedsMu.Unlock()
	if _, dup := feeds[kind]; dup || kind == KindCSV {
		panic("HR source " + kind + " registered twice")
	}
	feeds[kind] = f
}

func feedFactory(kind string) (FeedFactory, bool) {
	feedsMu.RLock()
	defer feedsMu.RUnlock()
	f, ok := feeds[kind]
	return f, ok
}

// Kinds lists the source kinds: csv and every registered HR system.
func Kinds() []string {
	feedsMu.RLock()
	defer feedsMu.RUnlock()
	out := []string{KindCSV}
	for k := range feeds {
		out = append(out, k)
	}
	sort.Strings(out[1:])
	return out
}

// Limits of how often an HR system is read on its own.
const (
	DefaultEveryHours = 24
	maxEveryHours     = 7 * 24
)

// validateFor checks a config for a source of kind, building its feed to
// check the connection, and fills in defaults.
func (c *Config) validateFor(kind string, secrets connector.SecretResolver) error {
	if err := c.Validate(); err != nil {
		return &apperr.Invalid{Msg: err.Error()}
	}
	if kind == KindCSV {
		if len(c.Connection) > 0 || c.EveryHours != 0 {
			return apperr.Invalidf("a csv source has no connection: it is fed with files")
		}
		return nil
	}
	if _, ok := feedFactory(kind); !ok {
		return apperr.Invalidf("kind must be one of %s", strings.Join(Kinds(), ", "))
	}
	switch {
	case c.EveryHours == 0:
		c.EveryHours = DefaultEveryHours
	case c.EveryHours < 1 || c.EveryHours > maxEveryHours:
		return apperr.Invalidf("every_hours must be between 1 and %d", maxEveryHours)
	}
	if _, err := openFeed(kind, *c, secrets); err != nil {
		return &apperr.Invalid{Msg: err.Error()}
	}
	return nil
}

func openFeed(kind string, cfg Config, secrets connector.SecretResolver) (Feed, error) {
	f, ok := feedFactory(kind)
	if !ok {
		return nil, fmt.Errorf("unknown HR source kind %q", kind)
	}
	conn := cfg.Connection
	if len(conn) == 0 {
		conn = json.RawMessage(`{}`)
	}
	return f(conn, secrets)
}

// rowsToCSV writes rows as a CSV file, with a header of every name any row
// has, so they can be read like an export.
func rowsToCSV(rows []map[string]string) ([]byte, error) {
	var header []string
	seen := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			if !seen[k] {
				seen[k] = true
				header = append(header, k)
			}
		}
	}
	slices.Sort(header)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		return nil, err
	}
	line := make([]string, len(header))
	for _, r := range rows {
		for i, k := range header {
			line[i] = r[k]
		}
		if err := w.Write(line); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// pullTimeout bounds how long reading an HR system may take.
const pullTimeout = 5 * time.Minute

// Pull reads a source's HR system and syncs what it found, like Sync does
// with a file. A read that fails is recorded as an invalid run and changes
// nothing.
func Pull(ctx context.Context, pool *pgxpool.Pool, tenantID, sourceID string, opts Options, secrets connector.SecretResolver) (SyncResult, error) {
	if !db.ValidUUID(sourceID) {
		return SyncResult{}, ErrNotFound
	}
	var (
		kind string
		cfg  Config
	)
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT kind, config FROM identity_sources WHERE id = $1`, sourceID).Scan(&kind, &cfg)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncResult{}, ErrNotFound
	}
	if err != nil {
		return SyncResult{}, err
	}
	if kind == KindCSV {
		return SyncResult{}, apperr.Invalidf("this source is fed with files: send it one")
	}
	// The HR system is read before the transaction, so a slow API holds no
	// locks.
	var file []byte
	feed, err := openFeed(kind, cfg, secrets)
	if err == nil {
		readCtx, cancel := context.WithTimeout(ctx, pullTimeout)
		var rows []map[string]string
		rows, err = feed.Rows(readCtx)
		cancel()
		if err == nil && len(rows) == 0 {
			// Nobody at all is far likelier a broken report than a company
			// with no people.
			err = errors.New("the HR system returned nobody")
		}
		if err == nil {
			file, err = rowsToCSV(rows)
		}
	}
	readErr := err
	var res SyncResult
	err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) (err error) {
		res, err = syncWith(ctx, tx, tenantID, sourceID, opts, func(kind string, cfg Config) ([]Record, []Issue, error) {
			if readErr != nil {
				return nil, nil, fmt.Errorf("could not read the HR system: %w", readErr)
			}
			return ParseCSV(bytes.NewReader(file), cfg)
		})
		return err
	})
	return res, err
}

// PullDue reads, on its own, every HR system source whose last run is older
// than its every_hours. Runs that fail or are refused are recorded and wait
// for the next time, or for someone to look.
func PullDue(ctx context.Context, pool *pgxpool.Pool, tenantID string, secrets connector.SecretResolver) ([]SyncResult, error) {
	var due []string
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT s.id FROM identity_sources s
			WHERE s.kind <> $1 AND NOT EXISTS (
			  SELECT 1 FROM identity_source_runs r
			  WHERE r.source_id = s.id AND NOT r.dry_run
			    AND r.created_at > now() - make_interval(hours => coalesce((s.config->>'every_hours')::int, $2)))
			ORDER BY s.name`, KindCSV, DefaultEveryHours)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, err
	}
	var out []SyncResult
	for _, id := range due {
		res, err := Pull(ctx, pool, tenantID, id, Options{Actor: "system:hr-sync"}, secrets)
		if err != nil {
			return out, err
		}
		out = append(out, res)
	}
	return out, nil
}
