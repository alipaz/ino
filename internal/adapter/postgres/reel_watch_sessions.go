package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"ino/internal/usecase"
)

func (writer *ReelWriter) PersistWatchProgress(ctx context.Context, userID, reelID int64, playbackID string, watchedMilliseconds int64) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, reelWriteTimeout)
	defer cancel()
	var persistedWatchedMilliseconds, durationMilliseconds int64
	err := pgx.BeginTxFunc(ctx, writer.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		duration, err := lockPublishedReelDuration(ctx, tx, reelID)
		if err != nil {
			return err
		}
		durationMilliseconds = duration

		// PostgreSQL evaluates GREATEST against the locked current row. A
		// conflicting playback ID for a different reel produces no returned row.
		const upsert = `
			INSERT INTO user_reel_watch_sessions AS session (user_id, reel_id, playback_id, watched_ms)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id, playback_id) DO UPDATE
			SET watched_ms = GREATEST(session.watched_ms, EXCLUDED.watched_ms)
			WHERE session.reel_id = EXCLUDED.reel_id
			RETURNING watched_ms`
		err = tx.QueryRow(ctx, upsert, userID, reelID, playbackID, watchedMilliseconds).Scan(&persistedWatchedMilliseconds)
		if errors.Is(err, pgx.ErrNoRows) {
			return usecase.ErrPlaybackReelConflict
		}
		if err != nil {
			return fmt.Errorf("persist watch progress: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return persistedWatchedMilliseconds, durationMilliseconds, nil
}
