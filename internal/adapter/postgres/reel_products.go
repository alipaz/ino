package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"ino/internal/domain"
)

type ReelProductReader struct {
	pool *pgxpool.Pool
}

func NewReelProductReader(pool *pgxpool.Pool) *ReelProductReader {
	return &ReelProductReader{pool: pool}
}

func (reader *ReelProductReader) ListPublishedReelProducts(ctx context.Context, reelID int64) ([]domain.Product, bool, error) {
	const query = `
		SELECT p.id, p.title, p.brand, p.image_url, p.price, p.category
		FROM reels AS r
		LEFT JOIN reel_products AS rp ON rp.reel_id = r.id
		LEFT JOIN products AS p ON p.id = rp.product_id
		WHERE r.id = $1 AND r.status = 'published'
		ORDER BY rp.display_position, rp.product_id`

	rows, err := reader.pool.Query(ctx, query, reelID)
	if err != nil {
		return nil, false, fmt.Errorf("query published reel products: %w", err)
	}
	defer rows.Close()

	var products []domain.Product
	found := false
	for rows.Next() {
		found = true
		var id, price pgtype.Int8
		var title, brand, imageURL, category pgtype.Text
		if err := rows.Scan(&id, &title, &brand, &imageURL, &price, &category); err != nil {
			return nil, false, fmt.Errorf("scan reel product: %w", err)
		}
		// A published reel with no associations yields one row with a NULL product ID.
		if !id.Valid {
			continue
		}
		products = append(products, domain.Product{
			ID: id.Int64, Title: title.String, Brand: brand.String,
			ImageURL: imageURL.String, Price: price.Int64, Category: category.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read published reel products: %w", err)
	}
	return products, found, nil
}
