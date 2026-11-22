# Architecture

Prompt 0 is an operational foundation. There are no feature models, domain
tables, use cases, migrations, seeds or cache clients yet.

## Directory map

| Location | Responsibility | Status |
| --- | --- | --- |
| `cmd/api/` | Executable composition, logging, signals, HTTP lifecycle | Implemented |
| `internal/config/` | Environment defaults and validation | Implemented |
| `internal/transport/httpapi/` | Gin router, operational HTTP responses, request/panic logging | Implemented |
| `internal/adapter/postgres/` | pgxpool creation, bounded startup readiness | Implemented |
| `internal/domain/` | Entities and domain invariants, standard Go only | Future; create with code |
| `internal/usecase/` | Business rules and application operations; consumer-owned ports | Future; create with code |
| `internal/adapter/redis/` | Redis infrastructure if a feature justifies it | Future; create with code |
| `migrations/` | Versioned PostgreSQL schema changes | Future; choose tooling later |
| `cmd/seed/` | Reproducible assessment seed entry point | Future; decide implementation later |
| `docs/` | Architecture, assessment contract and phase history | Implemented |

## Dependency direction

Future transport adapters call use cases; use cases depend on domain code.
Concrete persistence adapters implement interfaces defined near their use-case
consumers. `cmd/api` connects them. Domain and use cases must remain independent
of Gin, pgx, Redis, HTTP routing and infrastructure configuration. Handlers
translate HTTP; they do not own business rules. Adapters own infrastructure
details. Do not create generic repositories or interfaces before a consumer
requires them.

Currently `cmd/api` imports configuration, PostgreSQL and HTTP adapters. The
HTTP adapter receives a `func(context.Context) error` for operational readiness
and does not import pgx. This callback is not a business use-case abstraction.

## Startup and shutdown

1. Compose starts PostgreSQL and Redis, each with its own container health
   check and named data volume. Only the API publishes a host port.
2. Compose waits for both dependencies to be healthy before starting `api`.
   Redis health is verified by Compose; the application does not connect to it.
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

## References

- [Compose dependency readiness](https://docs.docker.com/compose/how-tos/startup-order/)
- [pgxpool lifecycle and Ping](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool)
- [Gin release and Go version requirements](https://github.com/gin-gonic/gin/releases/tag/v1.12.0)
