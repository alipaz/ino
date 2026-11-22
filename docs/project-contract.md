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
  behavior. Prompts 1 and 2 cover the product listing and interactions with HTTP and PostgreSQL tests.
- Small Clean Architecture boundaries; domain and use cases independent of
  Gin, pgx and Redis.

## Decisions for later phases

The following are deliberately unresolved and must be specified in an authorized
later prompt before implementation:

| Area | Decisions still needed |
| --- | --- |
| Schema details | Feed indexes and schema for later features |
| Feed ranking | Formula, inputs, tie-breakers and how interactions change scores |
| Cursor stability | Encoding, ordering, snapshot/change semantics and duplicates/omissions between pages |
| Endpoint behavior | Feed routes, payloads, validation, filtering, pagination bounds and identity |
| Concurrency | Feed consistency and future feature transaction boundaries |
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

## Prompt 2 completion status

**Complete (2026-11-22).** Go tests, vet, Compose configuration, and the
real PostgreSQL integration suite passed. An isolated Compose run applied
both migrations and verified the interaction and public product routes
against fixture rows.

Prompt 2 implements `POST` and `DELETE` for `/like` and `/save`, `POST /view`,
`POST /share`, and `PUT /watch-sessions/{playback_id}` under
`/api/v1/reels/{id}`. They require an existing mock caller from `X-User-ID`
and a published reel. Replaying an existing share is the exception and works
after archival. The product route stays public. See the
[README](../README.md#reel-interactions-shares-and-watch-progress) for request
examples, response fields, and status codes.

Like and save writes are idempotent set and clear operations. Views count
once per user, reel, and server UTC date. A single PostgreSQL transaction
changes the interaction row and its counter together. Share retries return
201 with the original ID, timestamp, and fields for the same sender, key,
and payload, even after archival. A different valid payload for that sender
and key returns 409. A fresh key for an archived reel returns 404. Playback IDs bind to one reel per user. Repeated and
out-of-order reports preserve the maximum time, and the percentage caps at
100. Watch progress leaves view counts unchanged.

Goose migration 00002 assumes an empty reel catalog on first application and
requires a positive duration for every new reel. It adds users, interactions,
shares, watch sessions, and reel counters. Feed ranking, personalization,
delivery or inbox features, full seed data, and Redis caching remain outside
this phase.
