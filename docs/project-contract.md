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
  behavior. Prompts 1 and 2 cover product listing and interactions. The caching phase adds
  integration coverage with Redis and PostgreSQL.
- Small Clean Architecture boundaries; domain and use cases independent of
  Gin, pgx and Redis.

## Decisions for later phases

The following are deliberately unresolved and must be specified in an authorized
later prompt before implementation:

| Area | Decisions still needed |
| --- | --- |
| Schema details | Indexes and schema for later features |
| Endpoint behavior | Contracts for any later features |
| Concurrency | Future feature transaction boundaries |
| Redis usage | Caching and invalidation policy for future features |
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

## Redis caching phase status

**Complete (2026-11-22).** Go tests, vet, Compose config, and the explicit
PostgreSQL and Redis integration suites passed. The isolated Compose smoke
test applied migration 00003, restarted the API with Redis stopped, and
verified fallback, durable writes, and projector recovery after Redis resumed.

The public products response keeps the same shape and order.
`GET /api/v1/reels/{id}/interaction-state` returns the caller's `liked` and
`saved` flags for a published reel. `GET /api/v1/me/interactions/recent`
returns up to 100 events in descending event-time and event-ID order. Both
private GETs require an existing `X-User-ID`. A warm GET makes no PostgreSQL
queries, including for identity and publication checks. A cold or unavailable
cache reads PostgreSQL with a bounded fallback. Unavailable or overloaded
fallback returns 503. See the [README](../README.md#redis-behavior-and-freshness)
for TTLs, failure behavior, and verification commands.

PostgreSQL owns writes and history. Migration 00003 captures effective
like/save changes, counted views, new shares, and the first 70% watch
threshold crossing in the same transactions. Earlier activity is outside
this history. Sessions that already qualified at migration are latched
without creating historical events. The watch reporting contract is unchanged.
A transactional outbox delivers invalidations and complete recent-activity
snapshots. Redis state can lag between commit and projection or expiry.

The share cache is populated only after commit. The same sender, key, reel,
and recipient replay the original 201 receipt even after archival. Reuse of
a key with a different valid payload returns 409. A new share of an archived
reel returns 404. PostgreSQL remains the authority for misses, concurrent
writes, and Redis outages.

Feed ranking, ordering, pagination, personalization, messaging, and
notifications remain outside this phase.

## Explore feed phase

**Complete (2026-11-23).** Go tests, vet, Compose configuration, and disposable
PostgreSQL and Redis integration suites passed. Warm public and authenticated
GETs made zero PostgreSQL queries. An isolated Compose run retained cursor
order after ranking and eligibility changed, including while Redis was stopped.

`GET /api/v1/feed` lists published reels with creator details, counters, and
all products. Category filters match any attached product. The route defaults
to 20 items and accepts up to 50. Omit `X-User-ID` for the public feed. Valid
viewers with useful signals receive personalization. Invalid identity returns
401.

V1 weights are share +5, active save +4, quality watch +3, active like +2,
simple view 0, and quick skip −2. Global ranking uses a signed logarithmic
engagement scale and 48-hour freshness half-life at 45% and 55%. The ranker
blends this with category affinity using up to 35% personal weight. The client
sends a final cumulative watch report when leaving a reel and reuses the
playback ID for retries. Scoring uses the maximum watch time per user and
reel. Watch signals are separate from the chronological activity history,
with boundaries at 20% and 70%. See the [feed guide](../README.md#explore-feed)
for exact formulas and request examples.

A signed cursor freezes the order of up to 200 reels for 10 minutes and is
bound to its viewer and category. Invalid cursors return 400 and expired
cursors return 410. Continuation pages keep the original order through
ranking changes and Redis outages. Details skip reels that no longer qualify.
Redis holds short-lived identity, order, and page entries. Catalog and activity
projection invalidate relevant entries. Fallback uses the existing shared
PostgreSQL limits and returns 503 when unavailable or overloaded.

This phase uses the existing schema, watch API and persistence. The full
assessment seed remains a later deliverable.
