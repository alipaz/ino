# API benchmark

## Workload

Measured on 2026-11-23 with the API and cache sync worker running alongside
PostgreSQL 18.6 and Redis 8.10.2 in Docker Desktop on macOS arm64. The Docker
VM reported 10 logical CPUs and 1.9 GiB of memory. The synthetic fixture
contains 100,000 reels (90,000 published), 1,200 products, and 512 mock users. Each scenario uses eight concurrent
clients and 256 requests, except cold-order and Redis-off feed scenarios,
which use 192 distinct viewers. Feed requests ask for 20 items and alternate
between filtered and unfiltered first pages.

Warm read scenarios prime the keys outside the timed window. The cold-order
scenario uses each viewer once, so the order cache is cold on a running
database. Write scenarios use initially untouched user/reel pairs. The hot
reel scenario sends Likes from distinct users to one reel. The mixed
scenario includes equal numbers of feed reads, product reads, Likes, and
Views. The Redis-off suite measures first-page feeds, product reads, and
Likes while the Redis service is stopped.

The client opens a local HTTP connection for each request and reads the
complete response body. Latency percentiles and throughput use successful
responses, and the runner also reports status counts. These runs characterize
short workloads on a synthetic catalog.

## Results

All 4,736 measured requests returned their expected success status.

### Read endpoints

Business routes use the `/api/v1` prefix. Health routes are at the root.

| Scenario | OK / requests | p50 (ms) | p95 (ms) | OK/s |
| --- | ---: | ---: | ---: | ---: |
| `GET /feed`, warm order | 256/256 | 3.5 | 7.3 | 2,034.5 |
| `GET /feed`, cold order | 192/192 | 54.1 | 85.7 | 130.6 |
| `GET /reels/{id}/products` | 256/256 | 1.8 | 4.1 | 3,789.4 |
| `GET /reels/{id}/interaction-state` | 256/256 | 1.9 | 3.7 | 3,706.7 |
| `GET /me/interactions/recent` | 256/256 | 2.6 | 4.7 | 2,701.2 |
| `GET /livez` | 256/256 | 1.8 | 3.2 | 4,008.7 |
| `GET /readyz` | 256/256 | 1.8 | 4.2 | 3,559.1 |

### Write endpoints

| Scenario | OK / requests | p50 (ms) | p95 (ms) | OK/s |
| --- | ---: | ---: | ---: | ---: |
| `POST /reels/{id}/like` | 256/256 | 3.2 | 4.5 | 2,271.6 |
| `DELETE /reels/{id}/like` | 256/256 | 3.1 | 7.0 | 2,226.0 |
| `POST /reels/{id}/save` | 256/256 | 3.2 | 5.2 | 2,344.1 |
| `DELETE /reels/{id}/save` | 256/256 | 3.1 | 5.4 | 2,346.2 |
| `POST /reels/{id}/view` | 256/256 | 3.7 | 7.4 | 1,890.4 |
| `POST /reels/{id}/share` | 256/256 | 5.2 | 10.7 | 1,296.2 |
| `PUT /reels/{id}/watch-sessions/{playback_id}` | 256/256 | 4.5 | 8.7 | 1,650.9 |

### Contention, mixed traffic, and Redis outage

| Scenario | OK / requests | p50 (ms) | p95 (ms) | OK/s |
| --- | ---: | ---: | ---: | ---: |
| Likes on one reel | 256/256 | 6.1 | 21.8 | 945.5 |
| Mixed feed, products, Like, and View | 256/256 | 3.3 | 7.6 | 2,043.3 |
| Feed with Redis stopped | 192/192 | 54.9 | 150.3 | 109.9 |
| Products with Redis stopped | 256/256 | 2.3 | 3.5 | 3,299.6 |
| Likes with Redis stopped | 256/256 | 2.8 | 4.4 | 2,683.9 |

The feed with Redis stopped had the highest read p95 in this run. Like writes
targeting one reel had higher tail latency than writes distributed across
reels. PostgreSQL queued 512 outbox rows during the outage. The worker
drained them to zero after Redis restarted.


## Reproduce

Run from the project root with Docker Compose, curl, and Python 3.10 or newer.
Use a free local port. The [fixture](../scripts/benchmark_fixture.sql) loads
only into an empty, migrated database in the disposable project. The
[benchmark client](../scripts/api_benchmark.py) prints one JSON record per
scenario.

```sh
(
  set -eu
  benchmark_reels=100000
  benchmark_port=18080
  benchmark_project="ino-api-bench-$(date +%s)-$$"
  bench_compose() {
    API_PORT="$benchmark_port" \
    DATABASE_URL='postgres://ino:ino_local@postgres:5432/ino?sslmode=disable' \
    POSTGRES_USER=ino POSTGRES_PASSWORD=ino_local POSTGRES_DB=ino \
    REDIS_URL='redis://redis:6379/0' CACHE_REDIS_TIMEOUT=50ms LOG_LEVEL=info \
      docker compose --env-file /dev/null -p "$benchmark_project" "$@"
  }
  pending_rows() {
    bench_compose exec -T postgres psql -X -qAt -U ino -d ino -v ON_ERROR_STOP=1 \
      -c 'SELECT count(*) FROM cache_outbox'
  }
  wait_for_outbox() {
    for attempt in $(seq 1 60); do
      if [ "$(pending_rows)" = 0 ]; then return 0; fi
      sleep 1
    done
    echo "Outbox did not drain within 60 seconds" >&2
    return 1
  }
  trap 'bench_compose down --volumes' EXIT

  bench_compose up -d --wait postgres redis
  bench_compose run --build --rm --no-deps migrate up
  bench_compose exec -T postgres psql -X -U ino -d ino -v reels="$benchmark_reels" \
    < scripts/benchmark_fixture.sql
  bench_compose up --build -d --no-deps api cache-sync-worker
  base_url="http://127.0.0.1:$benchmark_port"
  curl -fsS --retry 8 --retry-all-errors --retry-delay 1 "$base_url/readyz"
  printf '\n'

  python3 scripts/api_benchmark.py --base-url "$base_url" --suite standard
  wait_for_outbox

  bench_compose stop redis
  python3 scripts/api_benchmark.py --base-url "$base_url" --suite redis-off
  printf 'Pending outbox rows with Redis stopped: %s\n' "$(pending_rows)"
  bench_compose start redis
  wait_for_outbox
  printf 'Pending outbox rows after recovery: %s\n' "$(pending_rows)"
)
```
