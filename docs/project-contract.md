# INO Explore assessment contract

This document records known requirements and phase boundaries. It is not a
complete feature API specification. Read it alongside [architecture.md](architecture.md).

## Known final deliverable requirements

- Go with Gin, PostgreSQL, Redis and Docker Compose.
- A ranked reel feed with cursor pagination and category filtering.
- Products attached to reels.
- Like, save and view interactions.
- Mock identity supplied through `X-User-ID`.
- Correct behavior under concurrent requests and idempotent operations where
  required. Concrete per-operation guarantees must be specified before coding.
- Automated migrations and seed execution in the final deliverable.
- Seed minima: **20 creators, 200 reels, 100 products and 1,000 interactions**.
- Unit tests and tests against real PostgreSQL for persistence and concurrency
  behavior. Prompt 1 covers the product listing with HTTP and PostgreSQL tests.
- Small Clean Architecture boundaries; domain and use cases independent of
  Gin, pgx and Redis.

## Decisions for later phases

The following are deliberately unresolved and must be specified in an authorized
later prompt before implementation:

| Area | Decisions still needed |
| --- | --- |
| Schema details | Interaction tables, counters and indexes for later queries |
| Feed ranking | Formula, inputs, tie-breakers and how interactions change scores |
| Cursor stability | Encoding, ordering, snapshot/change semantics and duplicates/omissions between pages |
| View deduplication | Identity/window/key semantics, concurrent duplicates and counting rules |
| Endpoint behavior | Feed and interaction routes, payloads, validation, filtering, pagination bounds, identity and idempotency |
| Concurrency | Transaction boundaries, uniqueness guarantees, retries and conflict responses per operation |
| Redis usage | Which feature needs it, data ownership, consistency, invalidation and failure behavior; defer the Go client until then |
| Final automation | Seed command, startup ordering for seed data and reproducibility |

Each prompt settles only the part of this contract it authorizes. Retain prior
decisions unless the user explicitly changes them.

## Prompt 0 completion status

**Complete (2026-11-22).** Go tests, vet, Compose configuration, Docker build and
the isolated runtime smoke checks passed. Full results are recorded in
[the Prompt 0 plan](superpowers/plans/2026-11-22-prompt0.md).

Implemented: environment configuration, PostgreSQL startup/readiness through
pgxpool, Gin `GET /livez` and `GET /readyz`, structured logging, HTTP timeouts,
graceful shutdown/pool cleanup, version-pinned Docker build, Compose services
and health checks, named volumes, development defaults, and repository guidance.

Boundary: no feature endpoints, domain tables, migrations, seed data, ranking,
interaction logic, caching, workers, domain/use-case packages or Redis client.
Their eventual homes are documented without creating empty packages.

## Prompt 1 completion status

**Complete (2026-11-22).** Go tests, vet, Compose configuration, and the
PostgreSQL integration tests passed. An isolated Compose run verified migration
ordering, a fixture-backed HTTP response, and row persistence after restart.

Prompt 1 adds Goose-managed creators, reels, products, and reel-product
associations. The Compose `migrate` service applies pending migrations after
PostgreSQL becomes healthy and must exit successfully before API startup.
Inspect versions with `docker compose run --rm migrate status` or apply pending
versions with `docker compose run --rm --build migrate up`.

`GET /api/v1/reels/{id}/products` accepts a positive signed 64-bit decimal ID.
A published reel returns 200 and `{"products":[...]}` or `{"products":[]}`.
Product fields are `id`, `title`, `brand`, `image_url`, `price` in USD cents,
and `category` as a lowercase slug. Display position and product ID determine
ascending order. Invalid IDs return 400, missing and non-public reels return
404, and unexpected failures return a generic 500. The public endpoint ignores
`X-User-ID`.

To run the PostgreSQL integration suite, point `INO_TEST_DATABASE_URL` at a
test database and run `go test -count=1 ./internal/adapter/postgres`. See the
[README](../README.md#postgresql-integration-tests) for a disposable instance
and exact commands. Feed, interactions, full seed data and caching remain
outside Prompt 1.
