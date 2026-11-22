# INO Explore

A Go backend with Gin, PostgreSQL, Redis and Docker Compose. INO Explore
serves a ranked reel feed with optional personalization and cursor pagination.

Read [the project contract](docs/project-contract.md),
[architecture](docs/architecture.md) and [agent instructions](AGENTS.md) before
starting another phase.

## Start

Install Docker with Compose v2. No host Go installation or `.env` is needed:

```sh
docker compose up
```

Use `docker compose up --build` after changing Go code or SQL migrations.
The API listens at `http://127.0.0.1:8080`. PostgreSQL and Redis are reachable
as `postgres:5432` and `redis:6379` inside the Compose network; neither publishes
a host port. Once PostgreSQL is healthy, the one-shot `migrate` service applies
pending Goose migrations. The API starts after migration success and checks
its PostgreSQL connection before listening. It can start and serve requests
while Redis is unavailable.

## Configuration

Overrides are optional:

```sh
cp .env.example .env
# Edit .env, then recreate affected services:
docker compose up --build
```

| Variable | Default | Purpose |
| --- | --- | --- |
| `API_PORT` | `8080` | API host port, bound to localhost |
| `POSTGRES_USER` | `ino` | Development database role |
| `POSTGRES_PASSWORD` | `ino_local` | Development password, not a production secret |
| `POSTGRES_DB` | `ino` | Development database |
| `DATABASE_URL` | `postgres://ino:ino_local@postgres:5432/ino?sslmode=disable` | API connection string |
| `DB_STARTUP_TIMEOUT` | `30s` | Maximum PostgreSQL startup wait |
| `REDIS_URL` | Compose `redis://redis:6379/0` | Redis endpoint. For a host run, leave it empty to use PostgreSQL only. |
| `CACHE_REDIS_TIMEOUT` | `50ms` | Deadline per Redis operation |
| `FEED_CURSOR_SECRET` | Random per process | Signing key of at least 32 bytes. Set the same secret across API instances and restarts to preserve cursors. |
| `LOG_LEVEL` | `info` | `debug` includes cache hit, miss, fallback, and projector lag logs |

When changing `POSTGRES_*`, update `DATABASE_URL` to match and URI-encode reserved
characters in credentials. PostgreSQL initialization variables apply only to a
new data volume; existing roles/passwords must be changed in PostgreSQL itself.
`.env`, other local environment files and key/secret files are Git-ignored and
excluded from the Docker build context. Commit only example credentials.

For a host Go run (Go 1.25+), supply `DATABASE_URL` for a PostgreSQL instance
reachable from the host, run `go run ./cmd/migrate up`, then run `go run ./cmd/api`. The application requires
that variable and defaults `HTTP_ADDR` to `:8080`; Compose fixes the internal
address at `:8080`. `.env` is read by Compose, not by the Go binary.

The build pins Go 1.25.4 and Alpine 3.22.2; Compose pins PostgreSQL 18.6 and
Redis 8.10.2. The application uses go-redis v9, and Redis persists via AOF.

## Migrations

Goose runs the embedded SQL files in `migrations/`. API and migrator use the
same `DATABASE_URL`. Goose records applied versions in `goose_db_version`.
A normal `docker compose down` followed by `docker compose up` reuses the
named PostgreSQL volume and preserves existing rows.

Migration `00002_create_reel_interactions.sql` assumes an empty pre-launch
reel catalog on first application. Every reel must have a positive
`duration_ms`, and the migration supplies no default duration for older
rows. If your volume already contains reels, preserve it and plan a duration
backfill before applying migration 00002 there. Use a disposable database
for the examples and tests below.

```sh
docker compose logs migrate
docker compose run --rm migrate status
docker compose run --rm --build migrate up
```

## Explore feed

```sh
curl 'http://127.0.0.1:8080/api/v1/feed?limit=20'
curl -H 'X-User-ID: 1' 'http://127.0.0.1:8080/api/v1/feed?category=outdoors&limit=10'
curl -G -H 'X-User-ID: 1' 'http://127.0.0.1:8080/api/v1/feed' \
  --data-urlencode 'category=outdoors' --data-urlencode 'limit=10' \
  --data-urlencode "cursor=$NEXT_CURSOR"
```

Use an existing user ID for personalization. Omit the header for the public
feed. An invalid identity returns 401. Users without useful signals receive
public ranking. Category is a lowercase slug of at most 64 ASCII characters.
A matching published reel may have products in other categories, and its
response includes all attached products in display order. Feed items also
include creator, duration, and reel counters. An empty response is
`{"items":[],"next_cursor":null}`.

The default limit is 20 and the maximum is 50. Invalid parameters and
cursors return 400. An expired cursor returns 410. Use the same viewer and
category throughout a browse. A signed cursor freezes up to 200 ordered IDs
for 10 minutes from the first page. Its maximum encoded size is 2,291 bytes.
Continuation pages keep those IDs even if ranking changes or Redis is
unavailable. They skip reels that are no longer published or no longer
match the filter and advance past skipped positions. The feed does not
append newly eligible reels to an existing browse.

### Ranking V1 and final watch reports

V1 project heuristics are share **+5**, active save **+4**, quality watch
**+3**, active like **+2**, simple view **0**, and quick skip **−2**.
Candidates come from freshness, the unweighted sum of active likes and saves
for popular discovery, and up to five strongest positive categories. V1
weights apply after discovery. Public discovery reserves
100 fresh and 100 popular positions. Personalized discovery with preferred
categories reserves 70 fresh, 70 popular, and 60 category positions.
Buckets can overlap, so the deduplicated set may contain fewer than 200.

Global shares are deduplicated by sender and reel. The ranking input uses
the maximum stored watch time per user and reel. A watch below 20% is a
quick skip, 20% through just under 70% is neutral, and 70% or above is a
quality watch. The thresholds are evaluated without rounding percentages.
The client sends its final cumulative `watched_ms` to the existing
`PUT /api/v1/reels/{id}/watch-sessions/{playback_id}` when leaving the reel.
It sends no interim reports and reuses the playback ID for retries. The
existing `watch_interest` event adds no separate ranking signal.

Signed engagement is normalized with
`sign(score) * log1p(min(abs(score), 100)) / log1p(100)`. Freshness decays
with a 48-hour half-life. The base score is 55% freshness and 45% normalized
engagement. Personalization uses up to 100 recent activity events with a
seven-day half-life. A removal cancels the corresponding active like or save.
Untimestamped watch preferences are separate, weighted at 0.5, and drawn from
up to 100 watched reels ordered by reel creation time and ID. Signals split
across distinct categories. Affinities are normalized by the largest absolute
category total, and a candidate uses the mean of its category affinities.
The personal weight is the lesser of 0.35 and 0.07 times the number of reels
with a useful nonzero signal. Scores sort descending, then creation time and
ID descending.

### Feed cache behavior

Identity, first-page ordering, and materialized pages use Redis first. A
complete warm request makes zero PostgreSQL queries. Ordering lasts up to
15 seconds, and page content lasts up to 10 seconds. Committed catalog events
invalidate both. Committed activity events also invalidate first-page orders
through the projector. Watch reports that emit no outbox event may take up to
the order TTL to affect a new browse. Cached pages may briefly lag publication
or category edits until invalidation arrives or their TTL expires. Page fields
such as counters and creator names can lag until the page TTL expires.

Redis misses and failures fall back to bounded, coalesced PostgreSQL reads.
If a fallback cannot be admitted or PostgreSQL is unavailable, the request
returns 503. Redis is optional at startup. [Redis behavior and freshness](#redis-behavior-and-freshness)
describes the shared fallback limits and verification setup.

## List products for a published reel

```sh
curl -i http://127.0.0.1:8080/api/v1/reels/1/products
```

The endpoint accepts a decimal reel ID from 1 through 9223372036854775807.
A published reel returns HTTP 200 and `{"products":[...]}`. Each product has
`id`, `title`, `brand`, `image_url`, `price` (USD cents), and `category`
(a lowercase slug). Results are ordered by display position, then by product
ID, both ascending. A published reel with no products returns
`{"products":[]}`. A fresh database has no fixture rows, so the example
request returns 404 until reel 1 is inserted and published.

| Status | Meaning |
| --- | --- |
| 400 | Invalid reel ID |
| 404 | Missing, draft, or archived reel |
| 503 | PostgreSQL fallback unavailable or at capacity |
| 500 | Unexpected server failure |

Errors use `{"error":"..."}`. This endpoint is public and ignores `X-User-ID`.

## Reel interactions, shares, and watch progress

All routes in this table are under `/api/v1/reels/{id}`. Use one
`X-User-ID` header with the positive decimal ID of an existing user. The
header is mock identity. The product listing remains public.

| Method and path | Success | Retry behavior |
| --- | --- | --- |
| `POST /like`, `POST /save` | 204 | Sets one active like or save per user and reel. |
| `DELETE /like`, `DELETE /save` | 204 | Clears that active row, including when already absent. |
| `POST /view` | 204 | Counts once per user and reel per server UTC calendar date. |
| `POST /share` | 201 | Same sender, key, reel, and recipient replay the original receipt even after archival. A different valid payload for that sender and key returns 409. |
| `PUT /watch-sessions/{playback_id}` | 200 | Keeps the highest cumulative active watch time. Reusing the playback ID for another reel under the same user returns 409. |

Keys and playback IDs are case-sensitive and accept 1–128 ASCII characters
from `A–Z`, `a–z`, `0–9`, and `._~-`. JSON requests are limited to 2 KiB.
Invalid IDs, keys, and JSON return 400. Missing, malformed, or unknown caller
identity returns 401. A missing or non-published reel returns 404 unless the
request replays an existing share. An unknown recipient for a new share
returns 404. Self-sharing returns 400. Error responses use `{"error":"..."}`.
Username and email uniqueness use PostgreSQL `lower()` with the database
collation. Stored values retain case and cannot be blank or surrounded by
spaces.

With users 1 and 2 and a published reel 1 of duration 30,000 ms in a test database:

```sh
base=http://127.0.0.1:8080/api/v1/reels/1
curl -i -X POST "$base/like" -H 'X-User-ID: 1'
curl -i -X DELETE "$base/like" -H 'X-User-ID: 1'
curl -i -X POST "$base/view" -H 'X-User-ID: 1'
curl -i -X POST "$base/share" -H 'X-User-ID: 1' \
  -H 'Idempotency-Key: share-001' -H 'Content-Type: application/json' \
  -d '{"recipient_user_id":2}'
curl -i -X PUT "$base/watch-sessions/playback-001" -H 'X-User-ID: 1' \
  -H 'Content-Type: application/json' -d '{"watched_ms":21000}'
```

A share returns `id`, `sender_id`, `recipient_id`, `reel_id`,
`idempotency_key`, and the UTC `created_at` of the stored record. Use a new
key to intentionally resend while the reel is published. Replaying an existing
receipt preserves its ID, timestamp, and every response field. Watch progress
returns `{"watched_ms":21000,"watch_percentage":70}` for the example above. It
measures cumulative active playback in milliseconds. The percentage uses
the stored time and caps at 100. Watch reports do not count as views.

## Interaction state and recent activity

Both reads require `X-User-ID` for an existing user. With the fixtures above:

```sh
curl -fsS -H 'X-User-ID: 1' http://127.0.0.1:8080/api/v1/reels/1/interaction-state
curl -fsS -H 'X-User-ID: 1' http://127.0.0.1:8080/api/v1/me/interactions/recent
```

Interaction state returns `{"liked":false,"saved":false}` when both flags are
unset and returns 404 for a missing, draft, or archived reel. Recent activity
returns `{"events":[...]}`, or `{"events":[]}` for an empty history. Each event
has `id`, `user_id`, `reel_id`, `type`, and UTC `event_time`. It returns up
to 100 events ordered by event time descending, then event ID descending.
Missing or invalid identity returns 401. An overloaded or unavailable
PostgreSQL fallback returns `503 {"error":"read_unavailable"}`.

History begins with migration 00003. Events include `like_added`,
`like_removed`, `save_added`, `save_removed`, `view_counted`,
`share_created`, and `watch_interest`. The watch event records only the
first crossing of 70% in a playback session. A session that already reached
70% before migration is treated as having crossed the threshold without
creating a historical event. The watch API and duplicate-report behavior
stay the same.

## API and read projector

`docker compose up --build` starts two independent processes after migration.
Only `api` serves HTTP. Its PostgreSQL pool is capped at 10 connections, with
up to four concurrent cache fallbacks, and its Redis client is capped at 12
connections. The `read-projector` service uses its
own PostgreSQL pool capped at two connections: one for the outbox transaction
and one for a snapshot read. It also uses a separate Redis client capped at
two connections. Outbox batches have a five-second deadline, and PostgreSQL
snapshot reads have a 750 ms deadline. Both processes use
`CACHE_REDIS_TIMEOUT` for Redis calls.

With `DATABASE_URL` and `REDIS_URL` set, run the projector locally with
`go run ./cmd/read-projector`. SIGINT or SIGTERM cancels polling and any active
batch. Unacknowledged rows stay in PostgreSQL. Stop or start the Compose
projector independently with `docker compose stop read-projector` and
`docker compose start read-projector`. The API can keep serving and writing
while the projector is stopped.

## Redis behavior and freshness

| Entry | TTL |
| --- | ---: |
| Public reel products and publication state | 15 seconds |
| Interaction state and publication state | 10 seconds |
| Recent activity snapshot | 15 seconds |
| Mock identity existence | 15 seconds |
| Immutable share receipt | 5 minutes |

A warm GET reads Redis only, including its identity and reel visibility
checks. Valid empty results and false flags are cache hits. Missing,
incomplete, or unavailable entries fall back to PostgreSQL. Each API process
coalesces simultaneous loads of the same key and admits up to four concurrent
PostgreSQL fallbacks across keys. There is no waiting queue for those slots.
The PostgreSQL read deadline is 750 ms. Cache population is best effort.

PostgreSQL commits writes and activity together. Redis reads can lag while the
projector delivers committed invalidations. For example, a previously cached
published reel can briefly return its products after archival until the
catalog invalidation reaches Redis or the entry expires. Expiration is
measured from the fill and reads do not extend it. The projector polls every
250 ms when idle and retries after one second if Redis fails or the guarded
snapshot write cannot prove it is current. A complete, byte-identical
foreground fill in the same generation is acknowledged without refreshing
its TTL. Redis outages keep writes durable
and leave undelivered outbox work in PostgreSQL. Activity snapshots rebuild
from the ordered PostgreSQL history after Redis data loss.

Share requests still validate identity with PostgreSQL. A cached receipt
then bypasses the PostgreSQL share writer and returns the original response,
including after archival. The receipt is cached only after PostgreSQL
commits. An unavailable cache falls back to the existing transactional share
path, including its payload check and database uniqueness constraint.

## Smoke checks

In another terminal while the stack is running:

```sh
curl --fail http://127.0.0.1:8080/livez
curl --fail http://127.0.0.1:8080/readyz
docker compose exec -T redis redis-cli ping
docker compose ps
docker compose logs api
```

Expect HTTP 200 (`alive`, `ready`) and Redis `PONG`. Readiness returns 503 when
PostgreSQL cannot be pinged within one second; liveness stays 200 while the
process is serving. Redis health does not participate in application readiness.
Logs are structured JSON. SIGINT/SIGTERM drains HTTP before closing the pool.

## Verify changes

```sh
go test ./...
go vet ./...
docker compose config
```

Use a dedicated project name and a free port for isolated runtime checks so
existing containers and named volumes are unaffected (choose a unique name
for each run):

```sh
export COMPOSE_PROJECT_NAME=ino-cache-smoke-unique
export LOG_LEVEL=debug
export API_PORT=18080
docker compose up --build -d --wait --wait-timeout 120
curl --fail http://127.0.0.1:18080/livez
curl --fail http://127.0.0.1:18080/readyz
docker compose exec -T redis redis-cli ping
docker compose stop postgres
curl -i http://127.0.0.1:18080/readyz # Expect 503
curl --fail http://127.0.0.1:18080/livez # Still 200
docker compose start postgres
# Allow PostgreSQL to finish starting; /readyz should return 200 again.
curl --retry 10 --retry-all-errors --retry-delay 1 --fail http://127.0.0.1:18080/readyz
docker compose down --volumes # Deletes this disposable project's data only
unset COMPOSE_PROJECT_NAME API_PORT LOG_LEVEL
```

To check Redis fallback manually in that disposable stack, warm the three GETs
above against the chosen API port after loading test fixtures. Repeat them
and look for `cache read` logs with outcome `hit`. Run
`docker compose stop redis`, repeat the same GETs, and check that the bodies
match and the logs show `cache fallback`. Run `docker compose start redis` to
allow cache fills and queued invalidations to resume. Perform this check
before bringing the disposable stack down. The integration suite below
counts pgx queries during cold and warm HTTP requests to verify the zero-query
GET contract.

## PostgreSQL integration tests

The tests apply the same migrations to a temporary schema in a PostgreSQL
database and remove that schema afterward. The test role needs permission to
create schemas. Without `INO_TEST_DATABASE_URL`, `go test ./...` skips these
cases. The combined Redis tests also require `INO_TEST_REDIS_URL` pointing to
an empty, disposable Redis database. The suite flushes that test database.
Choose free local ports and unused container names:

```sh
docker run --rm -d --name ino-postgres-test \
  -e POSTGRES_PASSWORD=ino_test -p 127.0.0.1:15432:5432 postgres:18.6-alpine
# Wait for this command to report "accepting connections".
docker exec ino-postgres-test pg_isready -h 127.0.0.1 -U postgres
docker run --rm -d --name ino-redis-test -p 127.0.0.1:16379:6379 redis:8.10.2-alpine
INO_TEST_DATABASE_URL='postgres://postgres:ino_test@127.0.0.1:15432/postgres?sslmode=disable' \
INO_TEST_REDIS_URL='redis://127.0.0.1:16379/0' \
  go test -count=1 -p 1 -v ./internal/adapter/postgres ./internal/adapter/redis -run 'Postgres(Redis)?$|Redis$'
docker stop ino-postgres-test ino-redis-test
```

## Shutdown

For normal development, stop services and retain data:

```sh
docker compose down
```

`docker compose down --volumes` also deletes the selected project's database
data. Use it only when intentionally resetting that project's local data.

See the [Prompt 0 verification record](docs/superpowers/plans/2026-11-22-prompt0.md)
for checks actually run in this implementation session.
