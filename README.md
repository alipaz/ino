# INO Explore

A Go backend with Gin, PostgreSQL, Redis and Docker Compose. Prompt 2 adds
reel interactions, internal shares, and watch progress alongside the public
product listing.

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
pending Goose migrations. The API starts after migration success and Redis
health, then checks its own PostgreSQL connection before listening.

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
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

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
Redis 8.10.2. Redis persists via AOF but is not accessed by Go in this phase.

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
export COMPOSE_PROJECT_NAME=ino-prompt2-smoke-unique
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
unset COMPOSE_PROJECT_NAME API_PORT
```

## PostgreSQL integration tests

The tests apply the same migrations to a temporary schema in a PostgreSQL
database and remove that schema afterward. The test role needs permission to
create schemas. Without `INO_TEST_DATABASE_URL`, `go test ./...` skips these
cases. To run them against a disposable instance, choose a free local port
and unused container name:

```sh
docker run --rm -d --name ino-postgres-test \
  -e POSTGRES_PASSWORD=ino_test -p 127.0.0.1:15432:5432 postgres:18.6-alpine
# Wait for this command to report "accepting connections".
docker exec ino-postgres-test pg_isready -h 127.0.0.1 -U postgres
INO_TEST_DATABASE_URL='postgres://postgres:ino_test@127.0.0.1:15432/postgres?sslmode=disable' \
  go test -count=1 -v ./internal/adapter/postgres -run 'Postgres$'
docker stop ino-postgres-test
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
