-- +goose Up
CREATE INDEX reels_published_recency_idx
    ON reels (created_at DESC, id DESC) WHERE status = 'published';

CREATE INDEX reels_published_popularity_idx
    ON reels ((like_count::numeric + save_count) DESC, created_at DESC, id DESC)
    WHERE status = 'published';

-- +goose Down
DROP INDEX reels_published_popularity_idx;
DROP INDEX reels_published_recency_idx;
