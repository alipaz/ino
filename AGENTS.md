# INO Explore contributor guide

Start with the [README](README.md) for setup and API behavior. Use the
[architecture guide](docs/architecture.md) for request flows, transaction
boundaries, and cache recovery. Read the code and tests for the area you are
changing. Keep the change within the requested scope and preserve existing
work in the tree.

## Where to work

| Path | Responsibility |
| --- | --- |
| `cmd/api` | API wiring, dependencies, and HTTP lifecycle |
| `cmd/cache-sync-worker` | Runs the PostgreSQL outbox consumer and Redis projection |
| `internal/transport/httpapi` | Gin handlers, request validation, and response mapping |
| `internal/domain`, `internal/usecase`, `internal/ranking` | Domain types, application behavior, and feed scoring |
| `internal/adapter/postgres`, `internal/adapter/redis` | Persistence, caching, and outbox processing |
| `migrations`, `cmd/migrate`, `cmd/seed` | Goose migrations and assessment demo data |

## Design constraints

- Keep domain, use cases, and ranking independent of Gin, pgx, Redis, and
  infrastructure packages. Define an interface at its consumer when needed
  and compose the concrete implementations in `cmd/api`.
- PostgreSQL owns interactions, counters, and activity history. Preserve
  transaction boundaries and idempotency when modifying writes.
- The feed cache stores order. Page queries recheck current publication and
  category eligibility in PostgreSQL. Preserve cursor scope and ordering when
  changing pagination or cache behavior.
- When changing the schema, use a numbered Goose migration and check fresh
  startup and seed behavior. Keep local credentials in ignored environment
  files and document development defaults in `.env.example`.

## Verify

```sh
go test ./...
go vet ./...
docker compose config --services
```

Follow the [integration test instructions](README.md#verify) for changes to
PostgreSQL, Redis, or concurrency. Use a unique Compose project and a free API
port for runtime checks. Report the checks run and explain any that could
not run. Update the README
when public behavior or configuration changes. Record current API
performance measurements in the [benchmark guide](docs/benchmark.md).
