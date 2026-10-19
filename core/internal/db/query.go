package db

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grantra-dev/grantra/core/internal/apperr"
)

// QueryOne runs a query that returns one row and scans it, answering
// apperr.ErrNotFound when there is none.
func QueryOne[T any](ctx context.Context, tx pgx.Tx, scan pgx.RowToFunc[T], sql string, args ...any) (T, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		var zero T
		return zero, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, apperr.ErrNotFound
	}
	return v, err
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// SearchPattern turns what someone typed in a search box into an ILIKE
// pattern that finds it anywhere in a text, taken literally: % and _ match
// only themselves. q must be 1 to 500 characters once trimmed.
func SearchPattern(q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 500 {
		return "", apperr.Invalidf("q must be between 1 and 500 characters")
	}
	return "%" + likeEscaper.Replace(q) + "%", nil
}

// ContainsPattern is a LIKE pattern finding q, lowercased and taken
// literally, anywhere in a text: compare it with lower(column).
func ContainsPattern(q string) string {
	return "%" + likeEscaper.Replace(strings.ToLower(q)) + "%"
}

// WriteRows writes to w the bytes in the one column each row of a query
// returns, row by row, so something large (a session recording) never has
// to fit in memory. start is called before the first byte, to set headers.
func WriteRows(ctx context.Context, tx pgx.Tx, w io.Writer, start func(), sql string, args ...any) error {
	start()
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var data []byte
	for rows.Next() {
		if err := rows.Scan(&data); err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return rows.Err()
}

// IsUniqueViolation reports whether err is Postgres refusing a row that
// breaks a unique constraint, such as a name already in use.
func IsUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}
