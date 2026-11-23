package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"ino/internal/domain"
)

type FeedReader struct {
	pool *pgxpool.Pool
}

func NewFeedReader(pool *pgxpool.Pool) *FeedReader {
	return &FeedReader{pool: pool}
}

// The category qualifies the reel without restricting the products returned.
const feedCategoryMembership = `EXISTS (
	SELECT 1 FROM reel_products AS association
	JOIN products AS product ON product.id = association.product_id
	WHERE association.reel_id = r.id AND product.category = $1
)`

const feedCategoryFilter = `($1::text = '' OR ` + feedCategoryMembership + `)`

// Aggregate distinct categories exactly once per reel, even when several
// selected signals or attached products refer to the same category.
const feedProductCategories = `product_categories AS (
	SELECT association.reel_id, array_agg(DISTINCT product.category ORDER BY product.category) AS categories
	FROM (SELECT DISTINCT reel_id FROM selected_reels) AS selected
	JOIN reel_products AS association USING (reel_id)
	JOIN products AS product ON product.id = association.product_id
	GROUP BY association.reel_id
)`

// A reel outside a category's first 60 already has enough reels ahead of it
// in the preferred ordering to miss the 60-item bucket.
const feedUnfilteredPreferences = `
	SELECT recent.id
	FROM unnest($2::text[]) WITH ORDINALITY AS preference(category, position)
	CROSS JOIN LATERAL (
		SELECT e.id, e.created_at
		FROM eligible AS e
		WHERE EXISTS (
			SELECT 1 FROM reel_products AS association
			JOIN products AS product ON product.id = association.product_id
			WHERE association.reel_id = e.id AND product.category = preference.category
		)
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT 60
	) AS recent
	GROUP BY recent.id, recent.created_at
	ORDER BY min(preference.position), recent.created_at DESC, recent.id DESC
	LIMIT 60`

// With a category filter, the two category sets can be disjoint. Join them
// directly so a per-category recency scan does not exhaust the catalog.
const feedFilteredPreferences = `
	SELECT e.id
	FROM eligible AS e
	JOIN reel_products AS association ON association.reel_id = e.id
	JOIN products AS product ON product.id = association.product_id
	WHERE product.category = ANY($2::text[])
	GROUP BY e.id, e.created_at
	ORDER BY min(array_position($2::text[], product.category)), e.created_at DESC, e.id DESC
	LIMIT 60`

func (reader *FeedReader) LoadFeedCandidates(ctx context.Context, category string, preferred []string) ([]domain.FeedCandidate, error) {
	// Keep the unfiltered statement free of a correlated category lookup.
	categoryFilter := `$1::text = ''`
	preferredQuery := feedUnfilteredPreferences
	if category != "" {
		categoryFilter = feedCategoryMembership
		preferredQuery = feedFilteredPreferences
	}
	// Leave eligibility inlined so each discovery bucket can stop at its
	// indexed limit. Only the selected IDs and their facts are materialized.
	query := `
		WITH eligible AS NOT MATERIALIZED (
			SELECT r.id, r.created_at, r.like_count, r.save_count
			FROM reels AS r
			WHERE r.status = 'published' AND ` + categoryFilter + `
		),
		fresh AS (
			SELECT id FROM eligible
			ORDER BY created_at DESC, id DESC
			LIMIT CASE WHEN cardinality($2::text[]) > 0 THEN 70 ELSE 100 END
		),
		popular AS (
			SELECT id FROM eligible
			ORDER BY (like_count::numeric + save_count) DESC, created_at DESC, id DESC
			LIMIT CASE WHEN cardinality($2::text[]) > 0 THEN 70 ELSE 100 END
		),
		preferred AS (` + preferredQuery + `),
		discovered AS MATERIALIZED (
			SELECT id FROM fresh UNION SELECT id FROM popular UNION SELECT id FROM preferred
		),
		backfill AS (
			-- The first 200 eligible IDs contain enough unseen IDs to fill
			-- every open slot, even when all discovered IDs appear there.
			SELECT recent.id FROM (
				SELECT id, created_at FROM eligible
				ORDER BY created_at DESC, id DESC LIMIT $3
			) AS recent
			WHERE NOT EXISTS (SELECT 1 FROM discovered WHERE discovered.id = recent.id)
			ORDER BY recent.created_at DESC, recent.id DESC
			LIMIT ($3::bigint - (SELECT count(*) FROM discovered))
		),
		selected_reels (reel_id) AS MATERIALIZED (
			SELECT id FROM discovered UNION ALL SELECT id FROM backfill
		),
		candidate_details AS MATERIALIZED (
			SELECT r.id AS reel_id, r.created_at, r.duration_ms, r.like_count, r.save_count, r.view_count
			FROM selected_reels AS candidate JOIN reels AS r ON r.id = candidate.reel_id
		),
		` + feedProductCategories + `,
		share_counts AS (
			SELECT reel_id, count(DISTINCT sender_id) AS shares
			FROM selected_reels JOIN reel_shares USING (reel_id)
			GROUP BY reel_id
		),
		best_watches AS (
			SELECT reel_id, user_id, max(watched_ms) AS watched_ms
			FROM selected_reels JOIN user_reel_watch_sessions USING (reel_id)
			GROUP BY reel_id, user_id
		),
		watch_counts AS (
			SELECT reel_id,
				count(*) FILTER (WHERE watched_ms::numeric * 10 >= duration_ms::numeric * 7) AS quality,
				count(*) FILTER (WHERE watched_ms::numeric * 10 < duration_ms::numeric * 2) AS skips
			FROM best_watches JOIN candidate_details USING (reel_id)
			GROUP BY reel_id
		)
		SELECT r.reel_id, r.created_at, r.like_count, r.save_count, COALESCE(s.shares, 0), r.view_count,
			COALESCE(w.quality, 0), COALESCE(w.skips, 0), COALESCE(p.categories, ARRAY[]::text[])
		FROM candidate_details AS r
		LEFT JOIN share_counts AS s USING (reel_id)
		LEFT JOIN watch_counts AS w USING (reel_id)
		LEFT JOIN product_categories AS p USING (reel_id)`

	rows, err := reader.pool.Query(ctx, query, category, preferred, domain.MaxFeedCandidates)
	if err != nil {
		return nil, fmt.Errorf("query feed candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]domain.FeedCandidate, 0)
	for rows.Next() {
		var candidate domain.FeedCandidate
		if err := rows.Scan(&candidate.ID, &candidate.CreatedAt, &candidate.Likes, &candidate.Saves,
			&candidate.Shares, &candidate.Views, &candidate.QualityWatches, &candidate.QuickSkips,
			&candidate.Categories); err != nil {
			return nil, fmt.Errorf("scan feed candidate: %w", err)
		}
		candidate.CreatedAt = candidate.CreatedAt.UTC()
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read feed candidates: %w", err)
	}
	return candidates, nil
}

func (reader *FeedReader) LoadFeedSignals(ctx context.Context, viewerID int64) (domain.FeedSignals, error) {
	activity, err := reader.loadFeedActivity(ctx, viewerID)
	if err != nil {
		return domain.FeedSignals{}, err
	}
	watches, err := reader.loadFeedWatches(ctx, viewerID)
	if err != nil {
		return domain.FeedSignals{}, err
	}
	return domain.FeedSignals{Activity: activity, Watches: watches}, nil
}

func (reader *FeedReader) loadFeedActivity(ctx context.Context, viewerID int64) ([]domain.FeedActivity, error) {
	// Take the newest 100 rows before filtering, so ineligible or inactive
	// signals never cause older history to enter the personalization window.
	const query = `
		WITH latest AS MATERIALIZED (
			SELECT id, user_id, reel_id, event_type, event_time
			FROM activity_events
			WHERE user_id = $1
			ORDER BY event_time DESC, id DESC
			LIMIT 100
		),
		selected_reels AS (
			SELECT latest.*
			FROM latest JOIN reels AS r ON r.id = latest.reel_id AND r.status = 'published'
			WHERE latest.event_type NOT IN ('like_added', 'save_added') OR EXISTS (
				SELECT 1 FROM user_interactions AS active
				WHERE active.user_id = latest.user_id AND active.reel_id = latest.reel_id
				AND active.kind IN ('like', 'save')
				AND active.kind = CASE latest.event_type WHEN 'like_added' THEN 'like' ELSE 'save' END
			)
		),
		` + feedProductCategories + `
		SELECT a.id, a.user_id, a.reel_id, a.event_type, a.event_time, COALESCE(p.categories, ARRAY[]::text[])
		FROM selected_reels AS a LEFT JOIN product_categories AS p USING (reel_id)
		ORDER BY a.event_time DESC, a.id DESC`

	rows, err := reader.pool.Query(ctx, query, viewerID)
	if err != nil {
		return nil, fmt.Errorf("query feed activity signals: %w", err)
	}
	defer rows.Close()

	activity := make([]domain.FeedActivity, 0)
	for rows.Next() {
		var event domain.FeedActivity
		if err := rows.Scan(&event.ID, &event.UserID, &event.ReelID, &event.Type, &event.Time, &event.Categories); err != nil {
			return nil, fmt.Errorf("scan feed activity signal: %w", err)
		}
		event.Time = event.Time.UTC()
		activity = append(activity, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read feed activity signals: %w", err)
	}
	return activity, nil
}

func (reader *FeedReader) loadFeedWatches(ctx context.Context, viewerID int64) ([]domain.FeedWatch, error) {
	// Sessions have no timestamp. Use the 100 newest published watched reels
	// by reel creation time and ID, with one best final cumulative time each.
	const query = `
		WITH selected_reels AS MATERIALIZED (
			SELECT w.reel_id, max(w.watched_ms) AS watched_ms, r.duration_ms, r.created_at
			FROM user_reel_watch_sessions AS w
			JOIN reels AS r ON r.id = w.reel_id AND r.status = 'published'
			WHERE w.user_id = $1
			GROUP BY w.reel_id, r.duration_ms, r.created_at
			ORDER BY r.created_at DESC, w.reel_id DESC
			LIMIT 100
		),
		` + feedProductCategories + `
		SELECT w.reel_id, w.watched_ms, w.duration_ms, COALESCE(p.categories, ARRAY[]::text[])
		FROM selected_reels AS w LEFT JOIN product_categories AS p USING (reel_id)
		ORDER BY w.created_at DESC, w.reel_id DESC`

	rows, err := reader.pool.Query(ctx, query, viewerID)
	if err != nil {
		return nil, fmt.Errorf("query feed watch signals: %w", err)
	}
	defer rows.Close()

	watches := make([]domain.FeedWatch, 0)
	for rows.Next() {
		var watch domain.FeedWatch
		if err := rows.Scan(&watch.ReelID, &watch.WatchedMS, &watch.DurationMS, &watch.Categories); err != nil {
			return nil, fmt.Errorf("scan feed watch signal: %w", err)
		}
		watches = append(watches, watch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read feed watch signals: %w", err)
	}
	return watches, nil
}

func (reader *FeedReader) LoadFeedPage(ctx context.Context, category string, orderedIDs []int64, limit int) (domain.FeedPage, error) {
	page := domain.FeedPage{Items: make([]domain.FeedReel, 0, limit), Consumed: len(orderedIDs)}
	if len(orderedIDs) == 0 {
		return page, nil
	}
	// Preserve cursor-relative positions when reels are archived or no longer
	// match the filter. One extra eligible reel distinguishes the final page.
	const query = `
		WITH page_reels AS MATERIALIZED (
			SELECT ids.position, r.id, r.caption, r.video_url, r.thumbnail_url, r.created_at,
				r.duration_ms, r.like_count, r.save_count, r.view_count, r.creator_id
			FROM unnest($2::bigint[]) WITH ORDINALITY AS ids(id, position)
			JOIN reels AS r USING (id)
			WHERE r.status = 'published' AND ` + feedCategoryFilter + `
			ORDER BY ids.position
			LIMIT ($3::integer + 1)
		)
		SELECT r.position, r.id, r.caption, r.video_url, r.thumbnail_url, r.created_at, r.duration_ms,
			r.like_count, r.save_count, r.view_count, c.id, c.display_name,
			p.id, p.title, p.brand, p.image_url, p.price, p.category
		FROM page_reels AS r JOIN creators AS c ON c.id = r.creator_id
		LEFT JOIN reel_products AS rp ON r.id = rp.reel_id
		LEFT JOIN products AS p ON p.id = rp.product_id
		ORDER BY r.position, rp.display_position, rp.product_id`

	rows, err := reader.pool.Query(ctx, query, category, orderedIDs, limit)
	if err != nil {
		return domain.FeedPage{}, fmt.Errorf("query feed page: %w", err)
	}
	defer rows.Close()

	var lastPosition int64
	for rows.Next() {
		var position int64
		var item domain.FeedReel
		var productID, price pgtype.Int8
		var title, brand, image, productCategory pgtype.Text
		if err := rows.Scan(&position, &item.ID, &item.Caption, &item.VideoURL, &item.ThumbnailURL,
			&item.CreatedAt, &item.DurationMS, &item.LikeCount, &item.SaveCount, &item.ViewCount,
			&item.CreatorID, &item.CreatorName, &productID, &title, &brand, &image, &price,
			&productCategory); err != nil {
			return domain.FeedPage{}, fmt.Errorf("scan feed page row: %w", err)
		}
		if position != lastPosition {
			if len(page.Items) == limit {
				page.Consumed = int(lastPosition)
				break
			}
			item.CreatedAt = item.CreatedAt.UTC()
			item.Products = []domain.Product{}
			page.Items = append(page.Items, item)
			lastPosition = position
		}
		if productID.Valid {
			current := &page.Items[len(page.Items)-1]
			current.Products = append(current.Products, domain.Product{
				ID: productID.Int64, Title: title.String, Brand: brand.String,
				ImageURL: image.String, Price: price.Int64, Category: productCategory.String,
			})
		}
	}
	// The last eligible reel is a look-ahead marker; close drains its rows.
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.FeedPage{}, fmt.Errorf("read feed page: %w", err)
	}
	return page, nil
}
