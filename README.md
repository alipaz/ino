# INO Explore

A Go and Gin service for a ranked reel feed, linked products, and user
interactions. PostgreSQL stores the catalog and interactions. Redis accelerates
reads and is optional for serving requests.

## Run locally

Docker with Compose v2 is sufficient for the default setup.

```sh
docker compose up --build -d --wait --wait-timeout 120
curl -fsS --retry 8 --retry-all-errors --retry-delay 1 http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/livez
```

Liveness returns `200` while the API is serving. Readiness returns `503`
when PostgreSQL is unavailable.

Compose applies the [Goose migrations](migrations/) and runs the seed before
starting the API and `cache-sync-worker`. Only the API publishes a host port.
A fresh database contains 24 creators, 240 reels (200 published, 20 drafts,
20 archived), 120 products, 30 users, and 1,448 Like, Save, and View rows.
Media URLs are placeholders. The seed runs in a transaction, verifies
counts and counters, and skips databases that already contain application
data. `docker compose down` stops the stack and retains the database volume.

Use [`.env.example`](.env.example) for local overrides. Set `API_PORT` if port
8080 is occupied. Keep `DATABASE_URL` and the PostgreSQL settings consistent
when changing credentials. Set `FEED_CURSOR_SECRET` to the same value of at
least 32 bytes across API instances and restarts if cursors must survive
them. By default, each process generates its own signing key.

For a host Go run, export a reachable `DATABASE_URL` and optionally `REDIS_URL`.
With Go 1.25 or newer, run `go run ./cmd/migrate up`, then
`go run ./cmd/seed` and `go run ./cmd/api`. The API serves on `:8080` unless
`HTTP_ADDR` is set. The optional worker runs with
`go run ./cmd/cache-sync-worker`.

## API

Open [Swagger UI](http://127.0.0.1:8080/swagger/index.html) on the local API
to try the endpoints. Use **Authorize** and enter `1`, `2`, or `3` as the
`X-User-ID` on a freshly seeded stack. The
[OpenAPI document](internal/transport/httpapi/openapi.json) is also served at
`/swagger/openapi.json`.

The feed is public by default. Supply `X-User-ID` with a positive decimal ID
of an existing user to enable personalization. The same header is required
for writes, interaction state, and recent activity. It is mock identity for
this assessment. The product list is public.

### Try the seeded data

User 1 has outdoors history, user 2 has style history, and user 3 starts
without interactions. Available category slugs are `outdoors`, `style`,
`home-goods`, `tech`, `fitness`, and `drinkware`.

```sh
api=http://127.0.0.1:8080/api/v1
curl -fsS -H 'X-User-ID: 1' "$api/feed?category=outdoors&limit=5"
curl -fsS "$api/reels/1/products"
curl -i -X POST -H 'X-User-ID: 3' "$api/reels/1/like"
curl -i -X POST -H 'X-User-ID: 3' "$api/reels/1/like"
curl -i -X DELETE -H 'X-User-ID: 3' "$api/reels/1/like"
```

Copy a non-null `next_cursor` from the feed response into `NEXT_CURSOR`, then
send it with the same user and category.

```sh
curl -fsSG -H 'X-User-ID: 1' "$api/feed" \
  --data-urlencode 'category=outdoors' --data-urlencode 'limit=5' \
  --data-urlencode "cursor=$NEXT_CURSOR"
```

### Routes

The table uses paths relative to `/api/v1`.

| Method and path | Success | Contract |
| --- | --- | --- |
| `GET /feed` | 200 | Returns `{items, next_cursor}`. Optional query parameters are `limit` (1–50, default 20), `category` (lowercase slug, up to 64 characters), and `cursor`. |
| `GET /reels/{id}/products` | 200 | Returns `{products}`, including an empty array for a published reel without products. |
| `POST /reels/{id}/like`, `POST /reels/{id}/save` | 204 | Sets the caller's active Like or Save. Repetition leaves one active row. |
| `DELETE /reels/{id}/like`, `DELETE /reels/{id}/save` | 204 | Clears the caller's active Like or Save. An already absent interaction also returns 204. |
| `POST /reels/{id}/view` | 204 | Counts at most once per user and reel on each server UTC calendar date. |
| `POST /reels/{id}/share` | 201 | Requires `Idempotency-Key` and JSON `{"recipient_user_id":2}`. Repeating the same sender, key, reel, and recipient replays the stored receipt. |
| `PUT /reels/{id}/watch-sessions/{playback_id}` | 200 | Accepts JSON such as `{"watched_ms":10000}` for cumulative watch time. Returns the stored maximum and `watch_percentage`, capped at 100. The client reports on leaving a reel and reuses the playback ID on retry. |
| `GET /reels/{id}/interaction-state` | 200 | Returns `{liked, saved}` for the caller. |
| `GET /me/interactions/recent` | 200 | Returns `{events}` with up to 100 events, newest first, breaking timestamp ties by event ID. |

Feed items include creator, media URLs, creation time, duration, counters,
and products. Recent activity history starts with migration `00003`.
A category matches any attached product. The item still
includes all its products. Both product responses use `id`, `title`,
`brand`, `image_url`, `price` (USD cents), and `category`, ordered by
display position and then product ID.

Invalid inputs return `400`, invalid mock identity returns `401`, and
expired cursors return `410`. Share keys and playback IDs are case-sensitive
and allow 1–128 characters from `A–Z`, `a–z`, `0–9`, and `._~-`. Their JSON
bodies are limited to 2 KiB. Missing or unpublished reels generally return
`404` on reel routes. A stored share receipt can still be replayed after
archival, and product and interaction-state reads follow the cache behavior
described below. An unknown share recipient returns `404`. Reusing a share
key with a different valid payload or a playback ID for another reel returns
`409`. An unavailable or saturated PostgreSQL read path returns `503`.
Application errors use `{"error":"..."}`.

## Design

### Data model and concurrency

Go handlers call use cases through a small layered design. PostgreSQL stores
creators, reels, products, and interactions. `reel_products` models the
many-to-many product relationship and display order.

Like, Save, and View operations update the interaction and its counter in
one transaction. Uniqueness constraints enforce idempotency under concurrent
requests. See the [architecture guide](docs/architecture.md#data-and-transaction-boundaries)
for the locking rules, constraints, and outbox behavior.

#### Deferred asynchronous view counting

For higher view traffic, the preferred design would persist each view event
before acknowledging the request. A worker would deduplicate events by user,
reel, and the original UTC view date, then update the interaction and counter
in one transaction. This moves counting off the request path, with counts
reflecting new views after the worker processes them.

This design was deferred to fit the assessment timebox. The API currently
counts views synchronously in PostgreSQL. The existing `cache-sync-worker`
projects committed activity to Redis.

### Feed ranking and pagination

Discovery takes up to 200 published candidates from fresh and popular
groups. The public split is 100 fresh and 100 popular, using active Likes
plus Saves for popularity. Personalized discovery can reserve 60 slots
for up to five preferred categories, with 70 each for fresh and popular reels.
After deduplication, the newest remaining eligible reels fill the open
slots. Partial PostgreSQL indexes support freshness and popularity ordering.

The engagement signal `E` is +5 per share, +4 per active Save, +3 per quality
watch, +2 per active Like, 0 per View, and −2 per quick skip. Shares count
once per sender and reel. Watch scoring uses each viewer's maximum watch
time on the reel. Under 20% is a quick skip, 20% through just under 70% is
neutral, and 70% or more is a quality watch.

The base score is 55% freshness and 45% normalized engagement. Freshness
has a 48-hour half-life. Engagement normalization is
`sign(E) × log(1 + min(abs(E), 100)) / log(101)`. Personalization derives
category affinity from up to 100 recent activity events with a seven-day
half-life. A removal cancels the corresponding active Like or Save. It also
uses up to 100 watched reels, ordered by reel creation time and ID, at weight
0.5. Signals split across distinct categories. Affinity is normalized by the
largest absolute category total, and each reel uses its mean category affinity. The ranking
blends that mean with the base score at weight `min(0.35, 0.07 × useful_reels)`.
Ties break by creation time and then reel ID, both descending.

A signed cursor holds up to 200 ordered IDs and expires ten minutes from
the first page. It is bound to the user and category. Continuations keep
the initial order and skip reels that have since been archived or no longer
match the filter. A nonempty page reads current eligibility and details from
PostgreSQL even when the order is cached. A single browse covers up to 200
eligible reels, even in larger catalogs. Newly eligible reels can enter a
fresh browse after the order is refreshed.

### Cache and operations

Redis caches first-page order and selected reads. PostgreSQL holds durable
writes and tracks cache work for `cache-sync-worker`. Cache misses fall back
to bounded PostgreSQL reads. When the worker falls behind, cached products or
interaction state can briefly show the previous publication state. The feed
checks publication on every nonempty page. See the
[architecture guide](docs/architecture.md#cache-invalidation-and-recovery)
for invalidation, timeouts, and capacity limits.

Migration `00002` assumes an empty reel catalog on first application. An
existing catalog needs a duration backfill before that migration.

## Verify

```sh
go test ./...
go vet ./...
docker compose config --services
```

The integration suite runs with disposable PostgreSQL and Redis. It covers
concurrent and repeated interactions, rollback, seed re-runs, complete
seeded feed traversal, publication changes, and Redis fallback. PostgreSQL
tests create temporary schemas. The Redis suite flushes its test database,
so use a disposable instance. The example below uses Docker-assigned local
ports and removes only its two test containers.

```sh
(
  set -eu
  test_id="ino-test-$(date +%s)-$$"
  pg="$test_id-postgres"
  redis="$test_id-redis"
  trap 'docker stop "$pg" "$redis" >/dev/null 2>&1 || true' EXIT

  docker run --rm -d --name "$pg" -p 127.0.0.1::5432 \
    -e POSTGRES_PASSWORD=ino_test postgres:18.6-alpine
  docker run --rm -d --name "$redis" -p 127.0.0.1::6379 redis:8.10.2-alpine3.23

  for attempt in $(seq 1 30); do
    if docker exec "$pg" pg_isready -q -h 127.0.0.1 -U postgres &&
       docker exec "$redis" redis-cli ping | grep -qx PONG; then
      break
    fi
    sleep 1
  done
  docker exec "$pg" pg_isready -q -h 127.0.0.1 -U postgres
  docker exec "$redis" redis-cli ping | grep -qx PONG

  INO_TEST_DATABASE_URL="postgres://postgres:ino_test@$(docker port "$pg" 5432)/postgres?sslmode=disable" \
  INO_TEST_REDIS_URL="redis://$(docker port "$redis" 6379)/0" \
    go test -count=1 -p 1 ./...
)
```

See the [API benchmark](docs/benchmark.md) for measured latency, throughput,
and reproduction steps.

See the [contributor guide](AGENTS.md) for the code map, design constraints,
and verification checklist.
