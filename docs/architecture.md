# Architecture

The cache phase adds Redis-backed reads and share receipts to the existing
PostgreSQL catalog and write paths. The feed runs inside the API process.

## Directory map

| Location | Responsibility | Status |
| --- | --- | --- |
| `cmd/migrate/` | Goose migration and status commands with PostgreSQL session locking | Implemented |
| `cmd/read-projector/` | Runs the outbox projector with process-local PostgreSQL and Redis connections | Implemented |
| `cmd/api/` | Executable composition, logging, signals, HTTP lifecycle | Implemented |
| `internal/config/` | Environment defaults and validation | Implemented |
| `internal/transport/httpapi/` | Gin router, health and reel responses, activity reads, request/panic logging | Implemented |
| `internal/adapter/postgres/` | pgxpool lifecycle, catalog and activity reads, writes, and outbox claiming | Implemented |
| `internal/ranking/` | Pure Go ranking rules, score composition, and deterministic ordering | Implemented |
| `internal/domain/` | Products, interactions, shares, and activity events | Implemented |
| `internal/usecase/` | Catalog and interaction reads, writes, and consumer interfaces | Implemented |
| `internal/adapter/redis/` | Redis read decorators, share receipts, and the activity projector | Implemented |
| `migrations/` | Versioned Goose SQL and embedded migration files | Implemented |
| `cmd/seed/` | One-shot entry point for transactional assessment seeding | Implemented |
| `docs/` | Architecture, assessment contract and phase history | Implemented |

## Dependency direction

Transport adapters call use cases, and use cases depend on domain code.
Concrete persistence adapters implement interfaces defined near their use-case
consumers. `cmd/api` connects them. Domain and use cases must remain independent
of Gin, pgx, Redis, HTTP routing and infrastructure configuration. Handlers
translate HTTP; they do not own business rules. Adapters own infrastructure
details. Define interfaces when a consumer needs them.

`cmd/api` wires PostgreSQL-backed readers through Redis decorators and injects
them into the existing product use case and the new interaction-read use case.
The HTTP adapter receives a PostgreSQL ping function for operational readiness.
The Redis adapter owns the client, scripts, fallbacks, and projector. The
use-case interfaces expose domain results without referring to Redis or pgx.

## Startup and shutdown

1. Compose starts PostgreSQL and Redis, each with its own container health
   check and named data volume. Only the API publishes a host port.
2. After PostgreSQL becomes healthy over TCP, Compose runs `migrate` with the
   same database URL as `api`. Goose applies pending SQL migrations and tracks
   versions. The `seed` command runs next, using the same database URL.
   It serializes with other seed runs and commits only a validated dataset
   on an empty schema. Both `api` and `read-projector` wait for seed success
   and healthy PostgreSQL. Redis is optional for API startup.
3. The executable validates environment configuration and initializes JSON
   `slog` logging. It installs SIGINT/SIGTERM cancellation before database startup.
4. The PostgreSQL adapter parses `DATABASE_URL`, creates a pool capped at ten
   connections, and retries `Ping` until `DB_STARTUP_TIMEOUT` (default 30 seconds).
   Each connection/check is bounded by two seconds and the startup context.
   It closes the pool on failure. No listener opens before a successful ping.
5. The executable serves Gin on `HTTP_ADDR` with a 5-second header timeout,
   10-second read/write timeouts and a 60-second idle timeout.
6. `GET /livez` returns 200 with `{"status":"alive"}`. `GET /readyz` checks
   PostgreSQL using the request context and a one-second deadline, returning
   200 `{"status":"ready"}` or 503 `{"status":"not_ready"}`. No dependency
   details or credentials are returned. Readiness is computed per request.
7. SIGINT/SIGTERM stops accepting connections and gives active HTTP requests
   up to ten seconds to finish. If draining times out, connections are closed.
   The database pool is closed after HTTP shutdown. Compose allows 15 seconds
   before force-killing the API. Startup failures exit nonzero; signals during
   startup cancel the wait and exit cleanly.

Request, startup, shutdown, panic and server error logs use JSON. Request logs
omit headers and query strings; panic values and raw PostgreSQL errors are not
logged to avoid exposing credentials. Redis is outside operational readiness.

## Product listing query and indexes

The adapter uses one parameterized query filtered by reel ID and published
status. The left joins retain a published reel with zero products as one
row with a null product ID. Zero rows means no published reel matched.
The use case converts that absence to a not-found error, and the handler
maps it to HTTP 404. The handler forwards the request context through to pgx.

The `reels` and `products` primary-key indexes support ID lookup. The
`reel_products` primary key enforces one association per reel and product.
The separate `(reel_id, display_position, product_id)` index supports scanning
one reel's associations in the requested order, including position ties.
The query keeps explicit ordering regardless of PostgreSQL's plan.
Prices are integer USD cents, and category slugs contain lowercase ASCII letters
or digits separated by single hyphens.

## Reel mutations

The transport parses `X-User-ID` and checks the caller through its identity
reader before dispatching to the relevant use case. Private GETs cache the
existence check, and writes use PostgreSQL identity checks. The three write use cases own
operation semantics, including the UTC view date, self-share validation,
and percentage calculation from stored watch time.

- Likes, saves, and views take a PostgreSQL row lock on the published reel
  before changing interaction rows. Partial unique indexes enforce active
  like/save uniqueness and one view per UTC date. Affected row counts drive
  atomic counter increments or decrements in the same transaction. A counter
  failure rolls back the interaction change.
- Shares use a unique sender/key constraint and `INSERT ... ON CONFLICT DO
  NOTHING`. A retry reads the winning row in a new statement at Read Committed
  isolation, then compares the stored reel and recipient before replaying it.
- Watch sessions use the user/playback key. A conditional upsert keeps the
  stored reel binding and uses `GREATEST` to retain cumulative progress.
  Shares and watch reports use a shared reel lock to keep publication state
  and duration stable during their transactions.
- Identity lookups and write transactions have a three-second deadline
  bounded by the request context. PostgreSQL errors stay in the adapter's
  returned error chain. HTTP returns generic failures for unexpected errors.

Migration 00002 adds required reel duration without a backfill. Apply it
first to an empty reel catalog or prepare a separate backfill for existing
data. Username and email uniqueness are based on `lower()` in the database
collation.

## Cache fills and recent activity projection

Redis holds TTL-limited snapshots of products, interaction state, recent
activity, and the outcome of user existence checks. Product and interaction
snapshots include reel visibility. Both also use a shared catalog epoch, so
a catalog or publication change invalidates visibility together with product
contents. State invalidations apply to the affected user and reel. Identity
entries hold only an existence flag. A cold load claims a random generation
before querying PostgreSQL. The guarded Redis fill succeeds only if the
generation, current value, and applicable catalog epoch still match. An
invalidation, an eviction, or a competing fill can invalidate the claim.
This guards against an older database read repopulating an invalidated entry.

A single API process coalesces fallback work per key. It admits up to four
concurrent PostgreSQL reads with a deadline of 750 ms each, and rejects excess
fallback work with 503. An individual waiting request can cancel independently
of the shared load. Redis calls use a separate configurable deadline.
Successful reads and writes tolerate Redis connection and fill failures.

Migration 00003 uses PostgreSQL hooks to append activity and enqueue cache
work in the originating transaction. The watch threshold latch records the
first 70% crossing without changing the watch reporting path. Sessions already
at or above the threshold are marked without backfilling events. Catalog and
identity changes enqueue their own invalidations. Event IDs and event times
remain durable in PostgreSQL independently of cache delivery.

The separate `read-projector` process polls every 250 ms when idle. Its pool
has two PostgreSQL connections, one for the outbox transaction and one for a
snapshot read. Its Redis client is capped at two connections. The API has
its own PostgreSQL pool capped at ten connections, a Redis cap of twelve
connections, and a separate four-slot fallback limiter. Signals cancel the
projector's active batch before closing its clients. A batch claims at most 16
outbox rows with `FOR UPDATE SKIP LOCKED` inside a five-second transaction.
Redis success allows acknowledgement by deleting the processed outbox rows.
Failures roll back the acknowledgement and retry after one second. Delayed
and repeated jobs invalidate state rather than applying an old field value.
For recent activity they load the latest 100 durable rows ordered by event time
and ID descending. A compare-and-set writes the whole complete Redis snapshot
and applies the TTL atomically. If another request already installed exactly
the same complete snapshot in the same generation, the projector acknowledges
the batch without rewriting the value or extending the TTL. A changed
generation or a competing fill with a different value still triggers a retry.
Activity committed during a rebuild has its own outbox notification. Eviction
or data loss also permits an on-demand rebuild. Durable history is retained when outbox rows are acknowledged.

The share decorator caches immutable receipts after the writer commits, keyed
by sender and idempotency key. After request validation, a hit compares reel
and recipient before replaying the receipt. On a miss PostgreSQL keeps the
existing authoritative uniqueness and conflict handling. See the
[README](../README.md#redis-behavior-and-freshness) for TTLs and failure behavior.

## Explore feed

The feed use case composes batched PostgreSQL queries, the pure V1 ranker,
and an authenticated cursor. The first read loads activity and watch inputs
only on a cold personalized order. Discovery selects bounded fresh, popular,
and category buckets before aggregating global share and watch facts. Page SQL
uses cursor ordinality and current eligibility to fetch creators and products
without one call per reel.

The Redis adapter uses the shared read limiter and coalescing for feed order
and materialized page keys. Both depend on the catalog epoch. Signed-in
feed orders also depend on a short-lived viewer epoch. Activity deletes that
viewer's epoch across all categories without scanning keys. Fills compare
both epochs for those orders. Public orders and other viewers learn about
global engagement changes as their order TTL expires. The signed
cursor contains the ordered IDs, viewer, category, position and expiry, so
continuations need only page data. See the [feed guide](../README.md#explore-feed)
for the ranking and pagination contracts.

## References

- [Compose dependency readiness](https://docs.docker.com/compose/how-tos/startup-order/)
- [pgxpool lifecycle and Ping](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool)
- [Gin release and Go version requirements](https://github.com/gin-gonic/gin/releases/tag/v1.12.0)
