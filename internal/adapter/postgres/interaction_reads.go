package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ino/internal/domain"
)

type InteractionReader struct {
	pool *pgxpool.Pool
}

func NewInteractionReader(pool *pgxpool.Pool) *InteractionReader {
	return &InteractionReader{pool: pool}
}

func (reader *InteractionReader) ReadInteractionState(ctx context.Context, userID, reelID int64) (domain.InteractionState, bool, error) {
	const query = `
		SELECT EXISTS (
			SELECT 1 FROM user_interactions
			WHERE user_id = $1 AND reel_id = r.id AND kind = 'like'
		), EXISTS (
			SELECT 1 FROM user_interactions
			WHERE user_id = $1 AND reel_id = r.id AND kind = 'save'
		)
		FROM reels AS r
		WHERE r.id = $2 AND r.status = 'published'`

	var state domain.InteractionState
	err := reader.pool.QueryRow(ctx, query, userID, reelID).Scan(&state.Liked, &state.Saved)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InteractionState{}, false, nil
	}
	if err != nil {
		return domain.InteractionState{}, false, fmt.Errorf("read published reel interaction state: %w", err)
	}
	return state, true, nil
}

func (reader *InteractionReader) ListRecentInteractions(ctx context.Context, userID int64) ([]domain.ActivityEvent, error) {
	const query = `
		SELECT id, user_id, reel_id, event_type, event_time
		FROM activity_events
		WHERE user_id = $1
		ORDER BY event_time DESC, id DESC
		LIMIT 100`

	rows, err := reader.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("query recent interactions: %w", err)
	}
	defer rows.Close()

	events := make([]domain.ActivityEvent, 0)
	for rows.Next() {
		var event domain.ActivityEvent
		if err := rows.Scan(&event.ID, &event.UserID, &event.ReelID, &event.Type, &event.Time); err != nil {
			return nil, fmt.Errorf("scan recent interaction: %w", err)
		}
		event.Time = event.Time.UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read recent interactions: %w", err)
	}
	return events, nil
}
