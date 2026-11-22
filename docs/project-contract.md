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
  behavior. Prompt 0 unit tests cover operations/configuration only; feature
  and PostgreSQL integration suites are later-phase deliverables.
- Small Clean Architecture boundaries; domain and use cases independent of
  Gin, pgx and Redis.

## Decisions for later phases

The following are deliberately unresolved and must be specified in an authorized
later prompt before implementation:

| Area | Decisions still needed |
| --- | --- |
| Schema details | Entities, fields, relationships, constraints, indexes and migration tooling |
| Feed ranking | Formula, inputs, tie-breakers and how interactions change scores |
| Cursor stability | Encoding, ordering, snapshot/change semantics and duplicates/omissions between pages |
| View deduplication | Identity/window/key semantics, concurrent duplicates and counting rules |
| Endpoint behavior | Routes, methods, payloads, validation/errors, filtering, pagination bounds, `X-User-ID` rules and interaction idempotency |
| Concurrency | Transaction boundaries, uniqueness guarantees, retries and conflict responses per operation |
| Redis usage | Which feature needs it, data ownership, consistency, invalidation and failure behavior; defer the Go client until then |
| Final automation | Migration and seed commands, startup ordering and reproducibility |

Prompt 1 should settle the next authorized part of this contract; do not assume
it authorizes all remaining features. Retain prior decisions unless the user
explicitly changes them.

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
