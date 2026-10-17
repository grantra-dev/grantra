package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// BatchSize caps how many items go to Postgres in one batch.
const BatchSize = 1000

// SendBatch queues the statements of each item and reads the results back
// in order, BatchSize items at a time: one round trip per batch instead of
// one per statement.
func SendBatch[T any](ctx context.Context, tx pgx.Tx, items []T, queue func(*pgx.Batch, T), read func(pgx.BatchResults, T) error) error {
	for start := 0; start < len(items); start += BatchSize {
		chunk := items[start:min(start+BatchSize, len(items))]
		b := &pgx.Batch{}
		for _, it := range chunk {
			queue(b, it)
		}
		br := tx.SendBatch(ctx, b)
		for _, it := range chunk {
			if err := read(br, it); err != nil {
				br.Close() //nolint:errcheck
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
	}
	return nil
}

// QueueAudit queues the statement of Audit in a batch; read its result
// with ReadAudit.
func QueueAudit(b *pgx.Batch, tenantID, actor, action, targetType, targetID string, details map[string]any) {
	sql, args := auditInsert(tenantID, actor, action, targetType, targetID, details)
	b.Queue(sql, args...)
}

// ReadAudit reads the result of a statement QueueAudit queued.
func ReadAudit(br pgx.BatchResults, action string) error {
	if _, err := br.Exec(); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}
