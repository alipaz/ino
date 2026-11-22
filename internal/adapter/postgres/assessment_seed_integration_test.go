package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"ino/internal/adapter/postgres"
)

func TestAssessmentSeedConcurrentAndIdempotentPostgres(t *testing.T) {
	pool := openCatalogTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	type outcome struct {
		inserted bool
		err      error
	}
	start := make(chan struct{})
	var runners sync.WaitGroup
	var results [2]outcome
	for i := range results {
		runners.Go(func() {
			<-start
			results[i].inserted, results[i].err = postgres.SeedAssessment(ctx, pool)
		})
	}
	close(start)
	runners.Wait()

	inserted := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.inserted {
			inserted++
		}
	}
	if inserted != 1 {
		t.Fatalf("seed ran %d times, want one", inserted)
	}
	const counts = `
		SELECT (SELECT count(*) FROM creators), (SELECT count(*) FROM reels),
			(SELECT count(*) FROM products), (SELECT count(*) FROM user_interactions),
			(SELECT count(*) FROM user_interactions WHERE kind = 'view'
			 AND view_date <> (created_at AT TIME ZONE 'UTC')::date),
			(SELECT count(*) FROM activity_events)`
	var creators, reels, products, interactions, wrongDay, activity int
	if err := pool.QueryRow(ctx, counts).Scan(&creators, &reels, &products, &interactions, &wrongDay, &activity); err != nil {
		t.Fatal(err)
	}
	if creators != 24 || reels != 240 || products != 120 || interactions != 1448 || wrongDay != 0 || activity <= interactions {
		t.Fatalf("unexpected seed totals: creators=%d reels=%d products=%d interactions=%d wrong_view_dates=%d activity=%d",
			creators, reels, products, interactions, wrongDay, activity)
	}
}

func TestAssessmentSeedPreservesNonemptyDatabasePostgres(t *testing.T) {
	pool := openCatalogTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, "INSERT INTO products (title, brand, image_url, price, category) VALUES ('User product', 'User brand', 'image', 100, 'custom')"); err != nil {
		t.Fatal(err)
	}
	inserted, err := postgres.SeedAssessment(ctx, pool)
	if err != nil || inserted {
		t.Fatalf("seed result for nonempty database: inserted=%v err=%v", inserted, err)
	}
	var title string
	var count int
	err = pool.QueryRow(ctx, "SELECT min(title), count(*) FROM products").Scan(&title, &count)
	if err != nil || title != "User product" || count != 1 {
		t.Fatalf("existing product changed: title=%q count=%d err=%v", title, count, err)
	}
}

func TestAssessmentSeedRollsBackInconsistentCountersPostgres(t *testing.T) {
	pool := openCatalogTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Suppress reel updates without raising an SQL error so the seed's own
	// verification must detect the drift and undo the inserts and their hooks.
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION suppress_seed_counter_updates() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END; $$;
		CREATE TRIGGER suppress_seed_counter_updates BEFORE UPDATE ON reels
		FOR EACH ROW EXECUTE FUNCTION suppress_seed_counter_updates();`); err != nil {
		t.Fatal(err)
	}
	if inserted, err := postgres.SeedAssessment(ctx, pool); err == nil || inserted {
		t.Fatalf("seed with inconsistent counters: inserted=%v err=%v", inserted, err)
	}
	const query = `SELECT (SELECT count(*) FROM creators) + (SELECT count(*) FROM reels) +
		(SELECT count(*) FROM user_interactions) + (SELECT count(*) FROM activity_events) +
		(SELECT count(*) FROM cache_outbox)`
	var count int
	if err := pool.QueryRow(ctx, query).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed seed left %d rows, err=%v", count, err)
	}
}
