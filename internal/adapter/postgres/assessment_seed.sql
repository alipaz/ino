INSERT INTO creators (display_name)
SELECT format('Studio %s', lpad(n::text, 2, '0'))
FROM generate_series(1, 24) AS series(n) ORDER BY n;

INSERT INTO users (username, email)
SELECT username, username || '@example.test'
FROM (
    SELECT n, CASE n WHEN 1 THEN 'trail-fan' WHEN 2 THEN 'style-fan'
        WHEN 3 THEN 'new-viewer' ELSE format('viewer-%s', n) END AS username
    FROM generate_series(1, 30) AS series(n)
) AS demo
ORDER BY n;

INSERT INTO products (title, brand, image_url, price, category)
SELECT format('%s find %s', initcap(replace(category, '-', ' ')), n),
    format('Demo Brand %s', 1 + n % 8),
    format('https://example.com/ino/products/%s.jpg', n),
    499 + (n * 137) % 14500, category
FROM generate_series(1, 120) AS series(n)
CROSS JOIN LATERAL (
    SELECT (ARRAY['outdoors','style','home-goods','tech','fitness','drinkware'])[1 + (n - 1) % 6] AS category
) AS product
ORDER BY n;

WITH numbered_creators AS (SELECT id, row_number() OVER (ORDER BY id) AS ordinal FROM creators)
INSERT INTO reels (creator_id, caption, video_url, thumbnail_url, created_at, status, duration_ms)
SELECT numbered_creators.id, format('Explore collection %s', n),
    format('https://example.com/ino/reels/%s.mp4', n),
    format('https://example.com/ino/reels/%s.jpg', n),
    CURRENT_TIMESTAMP - (((n - 1) / 6) * 6 * interval '1 hour' + ((n - 1) % 6) * interval '10 minutes'),
    CASE WHEN n <= 200 THEN 'published' WHEN n <= 220 THEN 'draft' ELSE 'archived' END,
    15000 + (n * 137) % 45000
FROM generate_series(1, 240) AS series(n)
JOIN numbered_creators ON numbered_creators.ordinal = 1 + (n - 1) % 24
ORDER BY n;

WITH numbered_reels AS (SELECT id, row_number() OVER (ORDER BY id) AS ordinal FROM reels),
     numbered_products AS (SELECT id, row_number() OVER (ORDER BY id) AS ordinal FROM products)
INSERT INTO reel_products (reel_id, product_id, display_position)
SELECT reel.id, product.id, slot.position
FROM numbered_reels AS reel
CROSS JOIN (VALUES (0), (1)) AS slot(position)
JOIN numbered_products AS product
    ON product.ordinal = 1 + (reel.ordinal - 1 + 6 * slot.position) % 120
WHERE slot.position = 0 OR reel.ordinal % 3 = 0;

WITH numbered_reels AS (SELECT id, row_number() OVER (ORDER BY id) AS ordinal FROM reels),
     numbered_users AS (SELECT id, row_number() OVER (ORDER BY id) AS ordinal FROM users),
     engagement AS (
         SELECT viewer.id AS user_id, reel.id AS reel_id,
             (ARRAY['like','save','view'])[1 + (reel.ordinal + sample.offset_no) % 3] AS kind
         FROM numbered_reels AS reel
         CROSS JOIN LATERAL generate_series(1, 5 + reel.ordinal % 5) AS sample(offset_no)
         JOIN numbered_users AS viewer ON viewer.ordinal = 4 + (reel.ordinal + sample.offset_no) % 27
         WHERE reel.ordinal <= 200
         UNION ALL
         SELECT viewer.id, reel.id, signal.kind
         FROM numbered_reels AS reel
         JOIN numbered_users AS viewer ON viewer.ordinal = 1 + (reel.ordinal - 1) % 6
         CROSS JOIN (VALUES ('like'), ('save')) AS signal(kind)
         WHERE reel.ordinal <= 72 AND viewer.ordinal <= 2
     )
INSERT INTO user_interactions (user_id, reel_id, kind, view_date)
SELECT user_id, reel_id, kind,
    CASE WHEN kind = 'view' THEN (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date END
FROM engagement;

WITH counts AS (
    SELECT reel_id, count(*) FILTER (WHERE kind = 'like') AS likes,
        count(*) FILTER (WHERE kind = 'save') AS saves,
        count(*) FILTER (WHERE kind = 'view') AS views
    FROM user_interactions GROUP BY reel_id
)
UPDATE reels SET like_count = counts.likes, save_count = counts.saves, view_count = counts.views
FROM counts WHERE reels.id = counts.reel_id;

WITH numbered_reels AS (SELECT id, row_number() OVER (ORDER BY id) AS ordinal FROM reels),
     numbered_users AS (SELECT id, row_number() OVER (ORDER BY id) AS ordinal FROM users)
INSERT INTO reel_shares (sender_id, recipient_id, reel_id, idempotency_key)
SELECT sender.id, recipient.id, reel.id, format('assessment-%s', reel.ordinal)
FROM numbered_reels AS reel
JOIN numbered_users AS sender ON sender.ordinal = 1 + (reel.ordinal - 1) % 6
JOIN numbered_users AS recipient ON recipient.ordinal = 4
WHERE reel.ordinal IN (1, 2, 7, 8, 13, 14);

WITH numbered_reels AS (SELECT id, duration_ms, row_number() OVER (ORDER BY id) AS ordinal FROM reels),
     numbered_users AS (SELECT id, row_number() OVER (ORDER BY id) AS ordinal FROM users)
INSERT INTO user_reel_watch_sessions (user_id, reel_id, playback_id, watched_ms)
SELECT viewer.id, reel.id, format('assessment-play-%s', reel.ordinal),
    reel.duration_ms * example.percent / 100
FROM (VALUES (1, 1, 85), (1, 7, 90), (1, 13, 80), (2, 2, 85), (2, 8, 90), (2, 14, 80),
    (4, 1, 10), (4, 2, 45), (4, 3, 90)) AS example(viewer_no, reel_no, percent)
JOIN numbered_users AS viewer ON viewer.ordinal = example.viewer_no
JOIN numbered_reels AS reel ON reel.ordinal = example.reel_no;
