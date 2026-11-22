# Architecture

Prompt 2 builds on the operational foundation and public reel product listing
with persisted interactions, shares, and watch sessions.

## Directory map

| Location | Responsibility | Status |
| --- | --- | --- |
| `cmd/migrate/` | Goose migration and status commands with PostgreSQL session locking | Implemented |
| `cmd/api/` | Executable composition, logging, signals, HTTP lifecycle | Implemented |
| `internal/config/` | Environment defaults and validation | Implemented |
| `internal/transport/httpapi/` | Gin router, health, product, and interaction responses, request/panic logging | Implemented |
| `internal/adapter/postgres/` | pgxpool lifecycle, catalog reads, and reel write transactions | Implemented |
| `internal/domain/` | Product, interaction kind, and persisted share | Implemented |
| `internal/usecase/` | Product listing, interactions, shares, watch sessions, and consumer interfaces | Implemented |
| `internal/adapter/redis/` | Redis infrastructure if a feature justifies it | Future; create with code |
| `migrations/` | Versioned Goose SQL and embedded migration files | Implemented |
| `cmd/seed/` | Reproducible assessment seed entry point | Future; decide implementation later |
| `docs/` | Architecture, assessment contract and phase history | Implemented |

## Dependency direction

Transport adapters call use cases, and use cases depend on domain code.
Concrete persistence adapters implement interfaces defined near their use-case
consumers. `cmd/api` connects them. Domain and use cases must remain independent
of Gin, pgx, Redis, HTTP routing and infrastructure configuration. Handlers
translate HTTP; they do not own business rules. Adapters own infrastructure
details. Define interfaces when a consumer needs them.

Currently `cmd/api` imports configuration, PostgreSQL and HTTP adapters. The
HTTP adapter receives a `func(context.Context) error` for operational readiness
and does not import pgx. The product handler receives the `ReelProducts` use
case, which consumes its own `ReelProductReader` interface.

## Startup and shutdown

1. Compose starts PostgreSQL and Redis, each with its own container health
   check and named data volume. Only the API publishes a host port.
2. After PostgreSQL becomes healthy over TCP, Compose runs `migrate` with the
   same database URL as `api`. Goose applies pending SQL migrations and tracks
   versions. Compose waits for migration success and healthy PostgreSQL and
   Redis before starting the API. Redis health is verified by Compose.
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
logged to avoid exposing credentials. Redis is not part of Go readiness until
a later feature establishes a runtime dependency.

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
reader before dispatching to the relevant use case. Mock identity applies
only to the authenticated write routes. The three write use cases own
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

## References

- [Compose dependency readiness](https://docs.docker.com/compose/how-tos/startup-order/)
- [pgxpool lifecycle and Ping](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool)
- [Gin release and Go version requirements](https://github.com/gin-gonic/gin/releases/tag/v1.12.0)
