package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"ino/internal/domain"
	"ino/internal/usecase"
)

func (writer *ReelWriter) RecordReelShare(ctx context.Context, senderID, reelID, recipientID int64, idempotencyKey string) (domain.ReelShare, error) {
	ctx, cancel := context.WithTimeout(ctx, reelWriteTimeout)
	defer cancel()
	var share domain.ReelShare
	err := pgx.BeginTxFunc(ctx, writer.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		const columns = `id, sender_id, recipient_id, reel_id, idempotency_key, created_at`
		const existing = `SELECT ` + columns + ` FROM reel_shares WHERE sender_id = $1 AND idempotency_key = $2`
		scanShare := func(row pgx.Row) error {
			return row.Scan(&share.ID, &share.SenderID, &share.RecipientID, &share.ReelID, &share.IdempotencyKey, &share.CreatedAt)
		}
		replayReceipt := func() error {
			if err := scanShare(tx.QueryRow(ctx, existing, senderID, idempotencyKey)); err != nil {
				return fmt.Errorf("read share receipt: %w", err)
			}
			if share.ReelID != reelID || share.RecipientID != recipientID {
				return usecase.ErrShareKeyConflict
			}
			return nil
		}

		// An existing receipt remains replayable after the reel is archived.
		if err := replayReceipt(); !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := lockPublishedReelDuration(ctx, tx, reelID); err != nil {
			if errors.Is(err, usecase.ErrReelNotFound) {
				// A share may have committed before an archive operation won
				// the reel lock, after our first receipt lookup.
				if replayErr := replayReceipt(); !errors.Is(replayErr, pgx.ErrNoRows) {
					return replayErr
				}
			}
			return err
		}
		const lockRecipient = `SELECT id FROM users WHERE id = $1 FOR KEY SHARE`
		if err := tx.QueryRow(ctx, lockRecipient, recipientID).Scan(new(int64)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return usecase.ErrRecipientNotFound
			}
			return fmt.Errorf("check share recipient: %w", err)
		}

		const insert = `
			INSERT INTO reel_shares (sender_id, reel_id, recipient_id, idempotency_key)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (sender_id, idempotency_key) DO NOTHING
			RETURNING ` + columns
		err := scanShare(tx.QueryRow(ctx, insert, senderID, reelID, recipientID, idempotencyKey))
		if errors.Is(err, pgx.ErrNoRows) {
			// A new READ COMMITTED statement sees the concurrent winner.
			return replayReceipt()
		}
		if err != nil {
			return fmt.Errorf("store reel share: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.ReelShare{}, err
	}
	return share, nil
}

func lockPublishedReelDuration(ctx context.Context, tx pgx.Tx, reelID int64) (int64, error) {
	// Shares and watch reports can proceed concurrently while status and duration
	// remain stable until their transaction completes.
	const query = `SELECT duration_ms FROM reels WHERE id = $1 AND status = 'published' FOR SHARE`
	var durationMilliseconds int64
	if err := tx.QueryRow(ctx, query, reelID).Scan(&durationMilliseconds); err != nil {
		return 0, publishedReelError(err)
	}
	return durationMilliseconds, nil
}
