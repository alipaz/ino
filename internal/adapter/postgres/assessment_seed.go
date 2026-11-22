package postgres

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed assessment_seed.sql
var assessmentSeedSQL string

// SeedAssessment inserts the demo dataset only into an empty application schema.
// The advisory lock serializes seed commands before the emptiness check.
func SeedAssessment(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	const assessmentSeedLock int64 = 0x494e4f5f53454544 // INO_SEED
	inserted := false
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", assessmentSeedLock); err != nil {
			return fmt.Errorf("lock assessment seed: %w", err)
		}

		const hasApplicationData = `
			SELECT EXISTS (SELECT FROM creators) OR EXISTS (SELECT FROM reels)
				OR EXISTS (SELECT FROM products) OR EXISTS (SELECT FROM reel_products)
				OR EXISTS (SELECT FROM users) OR EXISTS (SELECT FROM user_interactions)
				OR EXISTS (SELECT FROM reel_shares) OR EXISTS (SELECT FROM user_reel_watch_sessions)
				OR EXISTS (SELECT FROM activity_events) OR EXISTS (SELECT FROM cache_outbox)`
		var populated bool
		if err := tx.QueryRow(ctx, hasApplicationData).Scan(&populated); err != nil {
			return fmt.Errorf("check assessment seed eligibility: %w", err)
		}
		if populated {
			return nil
		}

		if _, err := tx.Exec(ctx, assessmentSeedSQL); err != nil {
			return fmt.Errorf("insert assessment seed: %w", err)
		}
		if err := validateAssessmentSeed(ctx, tx); err != nil {
			return err
		}
		inserted = true
		return nil
	})
	return inserted && err == nil, err
}

func validateAssessmentSeed(ctx context.Context, tx pgx.Tx) error {
	const query = `
		WITH interaction_counts AS (
			SELECT reel_id, count(*) FILTER (WHERE kind = 'like') AS likes,
				count(*) FILTER (WHERE kind = 'save') AS saves,
				count(*) FILTER (WHERE kind = 'view') AS views
			FROM user_interactions GROUP BY reel_id
		)
		SELECT (SELECT count(*) FROM creators), (SELECT count(*) FROM reels),
			(SELECT count(*) FROM products), (SELECT count(*) FROM user_interactions),
			(SELECT count(DISTINCT status) FROM reels), (SELECT count(DISTINCT category) FROM products),
			(SELECT count(DISTINCT kind) FROM user_interactions),
			NOT EXISTS (SELECT FROM reels LEFT JOIN interaction_counts ON reels.id = interaction_counts.reel_id
				WHERE (like_count, save_count, view_count) <>
					(COALESCE(likes, 0), COALESCE(saves, 0), COALESCE(views, 0)))`
	var creators, reels, products, interactions, statuses, categories, kinds int
	var countersMatch bool
	err := tx.QueryRow(ctx, query).Scan(&creators, &reels, &products, &interactions, &statuses,
		&categories, &kinds, &countersMatch)
	if err != nil {
		return fmt.Errorf("validate assessment seed: %w", err)
	}
	if creators < 20 || reels < 200 || products < 100 || interactions < 1000 || statuses != 3 || categories != 6 || kinds != 3 {
		return fmt.Errorf("incomplete assessment dataset: %d creators, %d reels, %d products, %d interactions, %d statuses, %d categories, %d interaction kinds",
			creators, reels, products, interactions, statuses, categories, kinds)
	}
	if !countersMatch {
		return fmt.Errorf("assessment seed reel counters do not match interaction rows")
	}
	return nil
}
