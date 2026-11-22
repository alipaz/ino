# INO Explore

Prompt 0: a Go operational foundation with Gin, PostgreSQL, Redis and Docker
Compose. Only `GET /livez` and `GET /readyz` are implemented.

Read [the project contract](docs/project-contract.md),
[architecture](docs/architecture.md) and [agent instructions](AGENTS.md) before
starting another phase.

## Start

Install Docker with Compose v2. No host Go installation or `.env` is needed:

```sh
docker compose up --build
```

For subsequent starts, `docker compose up` also works from a clean checkout.
The API listens at `http://127.0.0.1:8080`. PostgreSQL and Redis are reachable
as `postgres:5432` and `redis:6379` inside the Compose network; neither publishes
a host port. Compose waits for dependency health checks, and the API also waits
for a successful PostgreSQL ping before opening its listener.

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
reachable from the host, then run `go run ./cmd/api`. The application requires
that variable and defaults `HTTP_ADDR` to `:8080`; Compose fixes the internal
address at `:8080`. `.env` is read by Compose, not by the Go binary.

The build pins Go 1.25.4 and Alpine 3.22.2; Compose pins PostgreSQL 18.6 and
Redis 8.10.2. Redis persists via AOF but is not accessed by Go in this phase.

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
export COMPOSE_PROJECT_NAME=ino-prompt0-smoke-unique
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

## Shutdown

For normal development, stop services and retain data:

```sh
docker compose down
```

`docker compose down --volumes` also deletes the selected project's database
data. Use it only when intentionally resetting that project's local data.

See the [Prompt 0 verification record](docs/superpowers/plans/2026-11-22-prompt0.md)
for checks actually run in this implementation session.
