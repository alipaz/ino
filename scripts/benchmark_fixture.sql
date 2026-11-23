-- Load into a fresh, migrated database reserved for this benchmark.
\set ON_ERROR_STOP on
BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT FROM creators) OR EXISTS (SELECT FROM users)
        OR EXISTS (SELECT FROM products) OR EXISTS (SELECT FROM reels) THEN
        RAISE EXCEPTION 'API benchmark requires an empty application database';
    END IF;
END
$$;

INSERT INTO creators (display_name)
SELECT 'Benchmark creator ' || n FROM generate_series(1, 100) AS series(n);

INSERT INTO users (username, email)
SELECT 'bench-' || n, 'bench-' || n || '@example.test'
FROM generate_series(1, 512) AS series(n) ORDER BY n;

INSERT INTO products (title, brand, image_url, price, category)
SELECT 'Benchmark product ' || n, 'Demo', 'https://example.test/image', 1000 + n,
    (ARRAY['outdoors','style','home-goods','tech','fitness','drinkware'])[1 + (n - 1) % 6]
FROM generate_series(1, 1200) AS series(n) ORDER BY n;

INSERT INTO reels (creator_id, caption, video_url, thumbnail_url, status, created_at, duration_ms)
SELECT 1 + (n - 1) % 100, 'Benchmark reel ' || n, 'https://example.test/video', 'https://example.test/image',
    CASE WHEN n % 20 = 0 THEN 'draft' WHEN n % 20 = 1 THEN 'archived' ELSE 'published' END,
    CURRENT_TIMESTAMP - n * interval '15 seconds', 30000
FROM generate_series(1, :reels) AS series(n) ORDER BY n;

INSERT INTO reel_products (reel_id, product_id, display_position)
SELECT id, 1 + (id - 1) % 1200, 0 FROM reels
UNION ALL
SELECT id, 1 + (id + 20) % 1200, 1 FROM reels WHERE id % 5 = 0;

INSERT INTO user_interactions (user_id, reel_id, kind)
SELECT 1 + (r.id + sample.n) % 256, r.id,
    CASE WHEN sample.n = 3 THEN 'save' ELSE 'like' END
FROM reels AS r CROSS JOIN LATERAL generate_series(1, (r.id % 4)::integer) AS sample(n)
WHERE r.status = 'published';

WITH counts AS (
    SELECT reel_id, count(*) FILTER (WHERE kind = 'like') AS likes,
        count(*) FILTER (WHERE kind = 'save') AS saves
    FROM user_interactions GROUP BY reel_id
)
UPDATE reels SET like_count = counts.likes, save_count = counts.saves
FROM counts WHERE reels.id = counts.reel_id;

INSERT INTO reel_shares (sender_id, recipient_id, reel_id, idempotency_key)
SELECT 1 + id % 256, 1 + (id + 1) % 256, id, 'bench-share-' || id
FROM reels WHERE status = 'published' AND id % 12 = 0;

INSERT INTO user_reel_watch_sessions (user_id, reel_id, playback_id, watched_ms)
SELECT 1 + id % 256, id, 'bench-watch-' || id, 2000 + (id * 31) % 28000
FROM reels WHERE status = 'published' AND id % 3 = 0;

-- The loader runs before the API and worker, against an empty Redis instance.
-- Keep durable history, then discard invalidations for cache entries that never existed.
DELETE FROM cache_outbox;

COMMIT;
VACUUM (ANALYZE) reels, products, reel_products, users, user_interactions,
    activity_events, reel_shares, user_reel_watch_sessions, cache_outbox;
