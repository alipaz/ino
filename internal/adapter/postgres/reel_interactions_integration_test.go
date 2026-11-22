package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"ino/internal/adapter/postgres"
	"ino/internal/domain"
	"ino/internal/usecase"
)

func TestReelInteractionsPostgres(t *testing.T) {
	ctx, pool, writers := openReelWriteTest(t)
	for _, kind := range []domain.InteractionKind{domain.Like, domain.Save} {
		t.Run(string(kind)+" concurrent set and repeated clear", func(t *testing.T) {
			runConcurrentReelWrites(t, func(i int) error {
				return writers[i%len(writers)].SetActiveReelInteraction(ctx, 1, 1, kind, true)
			})
			assertInteractionCount(t, ctx, pool, 1, string(kind), 1)
			runConcurrentReelWrites(t, func(i int) error {
				return writers[i%len(writers)].SetActiveReelInteraction(ctx, 1, 1, kind, false)
			})
			assertInteractionCount(t, ctx, pool, 1, string(kind), 0)
		})
	}
	t.Run("concurrent daily views and separate UTC days", func(t *testing.T) {
		utcDate := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
		runConcurrentReelWrites(t, func(i int) error {
			return writers[i%len(writers)].RecordReelView(ctx, 1, 1, utcDate)
		})
		assertInteractionCount(t, ctx, pool, 1, "view", 1)
		if err := writers[0].RecordReelView(ctx, 1, 1, utcDate.AddDate(0, 0, 1)); err != nil {
			t.Fatal(err)
		}
		assertInteractionCount(t, ctx, pool, 1, "view", 2)
	})

	t.Run("counter failure rolls back insert and delete", func(t *testing.T) {
		if err := writers[0].SetActiveReelInteraction(ctx, 1, 2, domain.Save, true); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(ctx, `
			ALTER TABLE reels ADD CONSTRAINT test_counter_failure
				CHECK (id <> 2 OR (like_count = 0 AND save_count = 1))`)
		if err != nil {
			t.Fatal(err)
		}
		for _, update := range []struct {
			kind   domain.InteractionKind
			active bool
			want   int64
		}{{domain.Like, true, 0}, {domain.Save, false, 1}} {
			err := writers[1].SetActiveReelInteraction(ctx, 1, 2, update.kind, update.active)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.ConstraintName != "test_counter_failure" {
				t.Fatalf("expected counter check violation, got %v", err)
			}
			assertInteractionCount(t, ctx, pool, 2, string(update.kind), update.want)
		}
	})
	t.Run("missing and non-public reels cannot be written", func(t *testing.T) {
		for _, reelID := range []int64{3, 4, 999} {
			errs := []error{
				writers[0].SetActiveReelInteraction(ctx, 1, reelID, domain.Like, true),
				writers[0].SetActiveReelInteraction(ctx, 1, reelID, domain.Save, false),
				writers[0].RecordReelView(ctx, 1, reelID, time.Now().UTC()),
			}
			_, shareErr := writers[0].RecordReelShare(ctx, 1, reelID, 2, "hidden-reel")
			_, _, watchErr := writers[0].PersistWatchProgress(ctx, 1, reelID, "hidden-reel", 10)
			for _, err := range append(errs, shareErr, watchErr) {
				if !errors.Is(err, usecase.ErrReelNotFound) {
					t.Fatalf("reel %d err = %v", reelID, err)
				}
			}
		}
	})
}

func TestReelSharesPostgres(t *testing.T) {
	ctx, pool, writers := openReelWriteTest(t)
	shares := make([]domain.ReelShare, concurrentReelWrites)
	runConcurrentReelWrites(t, func(i int) error {
		share, err := writers[i%len(writers)].RecordReelShare(ctx, 1, 1, 2, "share-retry")
		shares[i] = share
		return err
	})
	want := shares[0]
	if want.ID <= 0 || want.SenderID != 1 || want.RecipientID != 2 || want.ReelID != 1 || want.IdempotencyKey != "share-retry" || want.CreatedAt.IsZero() {
		t.Fatalf("unexpected saved share: %+v", want)
	}
	for _, share := range shares[1:] {
		if share != want {
			t.Fatalf("retry returned %+v, want %+v", share, want)
		}
	}

	var storedCount int
	err := pool.QueryRow(ctx, "SELECT count(*) FROM reel_shares WHERE sender_id = 1 AND idempotency_key = 'share-retry'").Scan(&storedCount)
	if err != nil || storedCount != 1 {
		t.Fatalf("stored shares = %d, err = %v", storedCount, err)
	}

	for _, payload := range []struct{ reelID, recipientID int64 }{{2, 2}, {1, 3}} {
		_, err := writers[1].RecordReelShare(ctx, 1, payload.reelID, payload.recipientID, "share-retry")
		if !errors.Is(err, usecase.ErrShareKeyConflict) {
			t.Fatalf("expected key conflict for %+v, got %v", payload, err)
		}
	}
	_, err = writers[1].RecordReelShare(ctx, 1, 1, 999, "unknown-recipient")
	if !errors.Is(err, usecase.ErrRecipientNotFound) {
		t.Fatalf("unknown recipient err = %v", err)
	}
	resend, err := writers[1].RecordReelShare(ctx, 1, 1, 2, "intentional-resend")
	if err != nil || resend.ID <= 0 || resend.ID == want.ID {
		t.Fatalf("resend = %+v, err = %v", resend, err)
	}
	otherSender, err := writers[1].RecordReelShare(ctx, 2, 1, 1, "share-retry")
	if err != nil || otherSender.ID <= 0 || otherSender.ID == want.ID {
		t.Fatalf("separate sender = %+v, err = %v", otherSender, err)
	}
}

func TestReelShareReplayAfterArchivalPostgres(t *testing.T) {
	ctx, pool, writers := openReelWriteTest(t)
	const key = "receipt-before-archive"
	original, err := writers[0].RecordReelShare(ctx, 1, 1, 2, key)
	if err != nil {
		t.Fatalf("create share before archival: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE reels SET status = 'archived' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	t.Run("identical retry returns the original receipt", func(t *testing.T) {
		replay, err := writers[1].RecordReelShare(ctx, 1, 1, 2, key)
		if err != nil || replay != original {
			t.Fatalf("replay = %+v, err = %v, want original receipt %+v", replay, err, original)
		}
	})
	t.Run("new key still requires a published reel", func(t *testing.T) {
		_, err := writers[1].RecordReelShare(ctx, 1, 1, 2, "fresh-after-archive")
		if !errors.Is(err, usecase.ErrReelNotFound) {
			t.Fatalf("fresh share on archived reel: got %v, want reel not found", err)
		}
	})
	t.Run("different recipient conflicts even for the archived reel", func(t *testing.T) {
		_, err := writers[1].RecordReelShare(ctx, 1, 1, 3, key)
		if !errors.Is(err, usecase.ErrShareKeyConflict) {
			t.Fatalf("changed recipient: got %v, want key conflict", err)
		}
	})
	t.Run("different reel conflicts", func(t *testing.T) {
		_, err := writers[1].RecordReelShare(ctx, 1, 2, 2, key)
		if !errors.Is(err, usecase.ErrShareKeyConflict) {
			t.Fatalf("changed reel: got %v, want key conflict", err)
		}
	})
	var storedCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM reel_shares").Scan(&storedCount); err != nil || storedCount != 1 {
		t.Fatalf("stored shares = %d, err = %v, want one share", storedCount, err)
	}
}

func TestReelWatchSessionsPostgres(t *testing.T) {
	ctx, pool, writers := openReelWriteTest(t)
	sessions := usecase.NewReelWatchSessions(writers[0])
	progress, err := sessions.UpdateWatchProgress(ctx, 1, 1, "playback", 21000)
	if err != nil || progress.WatchedMilliseconds != 21000 || progress.WatchPercentage != 70 {
		t.Fatalf("progress = %+v, err = %v", progress, err)
	}
	runConcurrentReelWrites(t, func(i int) error {
		_, _, err := writers[i%len(writers)].PersistWatchProgress(ctx, 1, 1, "playback", int64(i)*4000)
		return err
	})
	for _, report := range []int64{2000, 44000} {
		progress, err := sessions.UpdateWatchProgress(ctx, 1, 1, "playback", report)
		if err != nil || progress.WatchedMilliseconds != 44000 || progress.WatchPercentage != 100 {
			t.Fatalf("report %d gave progress %+v, err = %v", report, progress, err)
		}
	}
	_, err = sessions.UpdateWatchProgress(ctx, 1, 2, "playback", 99000)
	if !errors.Is(err, usecase.ErrPlaybackReelConflict) {
		t.Fatalf("playback conflict err = %v", err)
	}
	var reelID, watchedMilliseconds int64
	err = pool.QueryRow(ctx, "SELECT reel_id, watched_ms FROM user_reel_watch_sessions WHERE user_id = 1 AND playback_id = 'playback'").Scan(&reelID, &watchedMilliseconds)
	if err != nil || reelID != 1 || watchedMilliseconds != 44000 {
		t.Fatalf("stored reel = %d, time = %d, err = %v", reelID, watchedMilliseconds, err)
	}
	assertInteractionCount(t, ctx, pool, 1, "view", 0)
}

func TestUserIdentityConstraintsPostgres(t *testing.T) {
	ctx, pool, writers := openReelWriteTest(t)
	for _, tc := range []struct {
		id     int64
		exists bool
	}{{1, true}, {999, false}} {
		exists, err := writers[0].UserExists(ctx, tc.id)
		if err != nil || exists != tc.exists {
			t.Fatalf("user %d exists = %v, err = %v", tc.id, exists, err)
		}
	}
	for _, tc := range []struct{ username, email, constraint string }{
		{"ALICE", "unused@example.com", "users_username_normalized_key"},
		{"unused", "ALICE@EXAMPLE.COM", "users_email_normalized_key"},
	} {
		_, err := pool.Exec(ctx, "INSERT INTO users (id, username, email) VALUES (99, $1, $2)", tc.username, tc.email)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != tc.constraint {
			t.Fatalf("expected unique violation on %s, got %v", tc.constraint, err)
		}
	}
}

func openReelWriteTest(t *testing.T) (context.Context, *pgxpool.Pool, []*postgres.ReelWriter) {
	t.Helper()
	pool := openCatalogTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	const fixtures = `
		INSERT INTO users (id, username, email) VALUES
			(1, 'alice', 'alice@example.com'), (2, 'bob', 'bob@example.com'), (3, 'cee', 'cee@example.com');
		INSERT INTO creators (id, display_name) VALUES (1, 'Creator');
		INSERT INTO reels (id, creator_id, caption, video_url, thumbnail_url, status, duration_ms) VALUES
			(1, 1, '', 'video', 'thumbnail', 'published', 30000),
			(2, 1, '', 'video', 'thumbnail', 'published', 30000),
			(3, 1, '', 'video', 'thumbnail', 'draft', 30000),
			(4, 1, '', 'video', 'thumbnail', 'archived', 30000);`
	if _, err := pool.Exec(ctx, fixtures); err != nil {
		t.Fatal(err)
	}
	secondPool, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secondPool.Close)
	return ctx, pool, []*postgres.ReelWriter{postgres.NewReelWriter(pool), postgres.NewReelWriter(secondPool)}
}

const concurrentReelWrites = 12

func runConcurrentReelWrites(t *testing.T, write func(i int) error) {
	t.Helper()
	start := make(chan struct{})
	var writes sync.WaitGroup
	var errs [concurrentReelWrites]error
	for i := range errs {
		writes.Go(func() {
			<-start
			errs[i] = write(i)
		})
	}
	close(start)
	writes.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("write %d: %v", i, err)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}

func assertInteractionCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reelID int64, kind string, want int64) {
	t.Helper()
	const query = `
		SELECT CASE $2 WHEN 'like' THEN like_count WHEN 'save' THEN save_count ELSE view_count END,
			(SELECT count(*) FROM user_interactions WHERE reel_id = $1 AND kind = $2)
		FROM reels WHERE id = $1`
	var counter, rows int64
	err := pool.QueryRow(ctx, query, reelID, kind).Scan(&counter, &rows)
	if err != nil || counter != want || rows != want {
		t.Fatalf("reel %d %s counter = %d, rows = %d, want %d, err = %v", reelID, kind, counter, rows, want, err)
	}
}
