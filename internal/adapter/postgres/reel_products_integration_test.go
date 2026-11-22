package postgres_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"ino/internal/adapter/postgres"
	"ino/internal/domain"
	"ino/internal/usecase"
	"ino/migrations"
)

func TestListReelProductsPostgres(t *testing.T) {
	pool := openCatalogTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const fixtures = `
		INSERT INTO creators (id, display_name) VALUES (1, 'Creator');
		INSERT INTO reels (id, creator_id, caption, video_url, thumbnail_url, status, duration_ms) VALUES
			(1, 1, 'With products', 'video', 'thumbnail', 'published', 30000),
			(2, 1, 'Empty', 'video', 'thumbnail', 'published', 30000),
			(3, 1, 'Draft', 'video', 'thumbnail', 'draft', 30000),
			(4, 1, 'Archived', 'video', 'thumbnail', 'archived', 30000);
		INSERT INTO products (id, title, brand, image_url, price, category) VALUES
			(11, 'Tie first', 'INO', 'first.png', 725, 'home-goods'),
			(12, 'Tie second', 'INO', 'second.png', 0, 'home-goods'),
			(13, 'Position first', 'Maker', 'third.png', 1999, 'drinkware'),
			(14, 'Other reel', 'Maker', 'other.png', 500, 'drinkware');
		INSERT INTO reel_products (reel_id, product_id, display_position) VALUES
			(1, 12, 5), (1, 11, 5), (1, 13, 0), (3, 14, 0), (4, 14, 0);`
	if _, err := pool.Exec(ctx, fixtures); err != nil {
		t.Fatal(err)
	}
	reelProducts := usecase.NewReelProducts(postgres.NewReelProductReader(pool))

	t.Run("orders by position then product ID and maps all fields", func(t *testing.T) {
		want := []domain.Product{
			{ID: 13, Title: "Position first", Brand: "Maker", ImageURL: "third.png", Price: 1999, Category: "drinkware"},
			{ID: 11, Title: "Tie first", Brand: "INO", ImageURL: "first.png", Price: 725, Category: "home-goods"},
			{ID: 12, Title: "Tie second", Brand: "INO", ImageURL: "second.png", Price: 0, Category: "home-goods"},
		}
		got, err := reelProducts.ListReelProducts(ctx, 1)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("products = %+v, err = %v, want %+v", got, err, want)
		}
	})
	t.Run("published reel without associations is found", func(t *testing.T) {
		products, err := reelProducts.ListReelProducts(ctx, 2)
		if err != nil || len(products) != 0 {
			t.Fatalf("products = %+v, err = %v", products, err)
		}
	})
	for _, tc := range []struct {
		name string
		id   int64
	}{{"draft", 3}, {"archived", 4}, {"missing", 999}} {
		t.Run(tc.name+" reel is hidden", func(t *testing.T) {
			products, err := reelProducts.ListReelProducts(ctx, tc.id)
			if len(products) != 0 || !errors.Is(err, usecase.ErrReelNotFound) {
				t.Fatalf("products = %+v, err = %v", products, err)
			}
		})
	}
	t.Run("duplicate association is rejected", func(t *testing.T) {
		_, err := pool.Exec(ctx, "INSERT INTO reel_products VALUES (1, 11, 42)")
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "reel_products_pkey" {
			t.Fatalf("expected duplicate association violation, got %v", err)
		}
	})
}

func openCatalogTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("INO_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set INO_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to integration database: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})

	schema := pgx.Identifier{"ino_test_" + strings.ToLower(rand.Text())}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	db := stdlib.OpenDB(*cfg.ConnConfig)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
