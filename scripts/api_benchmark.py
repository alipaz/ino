"""Benchmark the current INO API on a disposable local stack."""

import argparse
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
import json
from math import ceil
import time
import urllib.error
import urllib.parse
import urllib.request


@dataclass
class Request:
    path: str
    method: str = "GET"
    expected: int = 200
    user: int | None = None
    body: dict | None = None
    key: str | None = None


def call(base_url, request):
    headers = {}
    if request.user is not None:
        headers["X-User-ID"] = str(request.user)
    if request.key is not None:
        headers["Idempotency-Key"] = request.key
    data = None
    if request.body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(request.body).encode()
    outgoing = urllib.request.Request(
        base_url + request.path, headers=headers, data=data, method=request.method
    )
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(outgoing, timeout=8) as response:
            status = response.status
            response.read()
    except urllib.error.HTTPError as error:
        status = error.code
        error.read()
        error.close()
    except (urllib.error.URLError, OSError):
        status = "transport_error"
    return status, time.perf_counter() - started, status == request.expected


def measure(base_url, name, requests, concurrency, prime=()):
    for request in prime:
        status, _, ok = call(base_url, request)
        if not ok:
            raise RuntimeError(f"could not prime {name}: {status} for {request.path}")

    def timed(request):
        return call(base_url, request)

    clients = min(concurrency, len(requests))
    started = time.perf_counter()
    with ThreadPoolExecutor(max_workers=clients) as executor:
        responses = list(executor.map(timed, requests))
    wall_seconds = time.perf_counter() - started
    latencies = sorted(elapsed * 1000 for _, elapsed, ok in responses if ok)
    statuses = Counter(str(status) for status, _, _ in responses)

    def percentile(fraction):
        return round(latencies[ceil(fraction * len(latencies)) - 1], 2) if latencies else None

    print(json.dumps({
        "scenario": name,
        "concurrency": clients,
        "requests": len(requests),
        "successful": len(latencies),
        "status_counts": dict(sorted(statuses.items())),
        "p50_ms": percentile(0.50),
        "p95_ms": percentile(0.95),
        "success_per_second": round(len(latencies) / wall_seconds, 1),
    }), flush=True)


def feed(user):
    category = "&category=outdoors" if user % 2 == 0 else ""
    return Request(f"/api/v1/feed?limit=20{category}", user=user)


def reel_request(reel, endpoint, method="GET", expected=200, user=None, body=None, key=None):
    return Request(f"/api/v1/reels/{reel}/{endpoint}", method, expected, user, body, key)


def standard_suite(run, count, concurrency):
    indexes = range(count)
    def repeat(requests):
        return [requests[i % len(requests)] for i in indexes]

    warm_feed = [feed(1 + i) for i in range(concurrency)]
    products = [reel_request(2 + 20 * i, "products") for i in range(concurrency)]
    states = [reel_request(2 + 20 * i, "interaction-state", user=1 + i) for i in range(concurrency)]
    activity = [Request("/api/v1/me/interactions/recent", user=1 + i) for i in range(concurrency)]

    run("feed-cold-order", [feed(65 + i) for i in range(min(count, 192))])
    for name, requests in (
        ("feed-warm-order", warm_feed),
        ("products", products),
        ("interaction-state", states),
        ("recent-activity", activity),
    ):
        run(name, repeat(requests), requests)
    for route in ("/livez", "/readyz"):
        run(route[1:], [Request(route) for _ in indexes])

    for method, endpoint in (
        ("POST", "like"),
        ("DELETE", "like"),
        ("POST", "save"),
        ("DELETE", "save"),
        ("POST", "view"),
    ):
        run(f"{method.lower()}-{endpoint}", [
            reel_request(22 + 20 * i, endpoint, method, 204, 257 + i) for i in indexes
        ])

    run("post-share", [
        reel_request(22 + 20 * i, "share", "POST", 201, 257 + i,
                     body={"recipient_user_id": 2}, key=f"bench-{i}")
        for i in indexes
    ])
    run("put-watch", [
        reel_request(22 + 20 * i, f"watch-sessions/bench-{i}", "PUT", 200, 257 + i,
                     body={"watched_ms": 25000})
        for i in indexes
    ])
    run("hot-reel-like", [reel_request(2, "like", "POST", 204, 257 + i) for i in indexes])

    mixed = []
    for i in indexes:
        reader = (i // 4) % concurrency
        mixed.append((
            warm_feed[reader],
            products[reader],
            reel_request(12 + 20 * i, "like", "POST", 204, 257 + i),
            reel_request(12 + 20 * i, "view", "POST", 204, 257 + i),
        )[i % 4])
    run("mixed", mixed, warm_feed + products)


def outage_suite(run, count):
    run("redis-off-feed", [feed(65 + i) for i in range(min(count, 192))])
    run("redis-off-products", [reel_request(2 + 20 * i, "products") for i in range(count)])
    run("redis-off-like", [
        reel_request(13 + 20 * i, "like", "POST", 204, 257 + i) for i in range(count)
    ])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:18080")
    parser.add_argument("--suite", choices=("standard", "redis-off"), required=True)
    parser.add_argument("--requests", type=int, default=256)
    parser.add_argument("--concurrency", type=int, default=8)
    args = parser.parse_args()
    url = urllib.parse.urlsplit(args.base_url)
    if url.scheme != "http" or url.hostname not in {"127.0.0.1", "localhost", "::1"}:
        parser.error("use a local HTTP benchmark API")
    if not 1 <= args.concurrency <= args.requests <= 256:
        parser.error("use 1 to 256 requests and concurrency no greater than the request count")
    base_url = args.base_url.rstrip("/")

    def run(name, requests, prime=()):
        measure(base_url, name, requests, args.concurrency, prime)

    if args.suite == "standard":
        standard_suite(run, args.requests, args.concurrency)
    else:
        outage_suite(run, args.requests)


if __name__ == "__main__":
    main()
