package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"ino/internal/domain"
	"ino/internal/usecase"
)

const reelWriteTimeout = 3 * time.Second

type ReelWriter struct {
	pool *pgxpool.Pool
}

func NewReelWriter(pool *pgxpool.Pool) *ReelWriter {
	return &ReelWriter{pool: pool}
}

func (writer *ReelWriter) SetActiveReelInteraction(ctx context.Context, userID, reelID int64, kind domain.InteractionKind, active bool) error {
	var counter string
	switch kind {
	case domain.Like:
		counter = "like_count"
	case domain.Save:
		counter = "save_count"
	default:
		return fmt.Errorf("unsupported active interaction %q", kind)
	}

	return writer.updateReelInteraction(ctx, reelID, counter, func(ctx context.Context, tx pgx.Tx) (int64, error) {
		const insert = `
			INSERT INTO user_interactions (user_id, reel_id, kind)
			VALUES ($1, $2, $3)
			ON CONFLICT (user_id, reel_id, kind) WHERE kind IN ('like', 'save') DO NOTHING`
		const remove = `
			DELETE FROM user_interactions WHERE user_id = $1 AND reel_id = $2 AND kind = $3`
		var result pgconn.CommandTag
		var err error
		if active {
			result, err = tx.Exec(ctx, insert, userID, reelID, string(kind))
			return result.RowsAffected(), err
		}
		result, err = tx.Exec(ctx, remove, userID, reelID, string(kind))
		return -result.RowsAffected(), err
	})
}

func (writer *ReelWriter) RecordReelView(ctx context.Context, userID, reelID int64, utcDate time.Time) error {
	return writer.updateReelInteraction(ctx, reelID, "view_count", func(ctx context.Context, tx pgx.Tx) (int64, error) {
		const insert = `
			INSERT INTO user_interactions (user_id, reel_id, kind, view_date)
			VALUES ($1, $2, 'view', $3)
			ON CONFLICT (user_id, reel_id, view_date) WHERE kind = 'view' DO NOTHING`
		result, err := tx.Exec(ctx, insert, userID, reelID, pgtype.Date{Time: utcDate, Valid: true})
		return result.RowsAffected(), err
	})
}

func (writer *ReelWriter) updateReelInteraction(ctx context.Context, reelID int64, counter string, change func(context.Context, pgx.Tx) (int64, error)) error {
	ctx, cancel := context.WithTimeout(ctx, reelWriteTimeout)
	defer cancel()
	return pgx.BeginTxFunc(ctx, writer.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		// Lock the published reel before touching interaction rows. All counter
		// mutations on that reel take locks in the same order.
		const lockReel = `SELECT id FROM reels WHERE id = $1 AND status = 'published' FOR NO KEY UPDATE`
		if err := tx.QueryRow(ctx, lockReel, reelID).Scan(new(int64)); err != nil {
			return publishedReelError(err)
		}
		delta, err := change(ctx, tx)
		if err != nil {
			return fmt.Errorf("change reel interaction: %w", err)
		}
		if delta == 0 {
			return nil
		}

		// The callers supply only fixed counter names. Values remain parameters.
		column := pgx.Identifier{counter}.Sanitize()
		result, err := tx.Exec(ctx, "UPDATE reels SET "+column+" = "+column+" + $1 WHERE id = $2", delta, reelID)
		if err != nil {
			return fmt.Errorf("update reel %s: %w", counter, err)
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("update reel %s: expected one updated row", counter)
		}
		return nil
	})
}

func publishedReelError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return usecase.ErrReelNotFound
	}
	return fmt.Errorf("lock published reel: %w", err)
}
