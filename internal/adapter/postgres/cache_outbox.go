package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ino/internal/domain"
)

type CacheOutbox struct {
	pool *pgxpool.Pool
}

func NewCacheOutbox(pool *pgxpool.Pool) *CacheOutbox {
	return &CacheOutbox{pool: pool}
}

// ProcessBatch holds the selected row locks until Redis processing and acknowledgment
// succeed. A callback error or an ambiguous commit permits retry, so apply must be idempotent.
func (outbox *CacheOutbox) ProcessBatch(ctx context.Context, apply func(context.Context, []domain.CacheChange) error) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var applied int
	err := pgx.BeginTxFunc(ctx, outbox.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		changes, err := lockPendingCacheChanges(ctx, tx)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		if err := apply(ctx, changes); err != nil {
			return fmt.Errorf("apply pending cache changes: %w", err)
		}

		ids := make([]int64, len(changes))
		for i, change := range changes {
			ids[i] = change.ID
		}
		result, err := tx.Exec(ctx, `DELETE FROM cache_outbox WHERE id = ANY($1::bigint[])`, ids)
		if err != nil {
			return fmt.Errorf("acknowledge cache changes: %w", err)
		}
		if result.RowsAffected() != int64(len(changes)) {
			return fmt.Errorf("acknowledged %d cache changes, expected %d", result.RowsAffected(), len(changes))
		}
		applied = len(changes)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("process cache outbox batch: %w", err)
	}
	return applied, nil
}

func lockPendingCacheChanges(ctx context.Context, tx pgx.Tx) ([]domain.CacheChange, error) {
	const query = `
		SELECT id, kind, COALESCE(user_id, 0), COALESCE(reel_id, 0), created_at
		FROM cache_outbox
		ORDER BY id
		LIMIT 16
		FOR UPDATE SKIP LOCKED`

	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("lock pending cache changes: %w", err)
	}
	defer rows.Close()

	changes := make([]domain.CacheChange, 0, 16)
	for rows.Next() {
		var change domain.CacheChange
		if err := rows.Scan(&change.ID, &change.Kind, &change.UserID, &change.ReelID, &change.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pending cache change: %w", err)
		}
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pending cache changes: %w", err)
	}
	return changes, nil
}
