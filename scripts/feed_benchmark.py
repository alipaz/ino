"""Measure Explore feed throughput and p95 on a local benchmark API."""

import argparse
import collections
import concurrent.futures
import json
import math
import time
import urllib.error
import urllib.parse
import urllib.request


def read_feed(base_url, user):
    # Exercise an even split of filtered and unfiltered authenticated feeds.
    category = "&category=outdoors" if user % 2 == 0 else ""
    request = urllib.request.Request(
        f"{base_url}/api/v1/feed?limit=20{category}",
        headers={"X-User-ID": str(user)},
    )
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=8) as response:
            status = response.status
            payload = json.load(response)
    except urllib.error.HTTPError as response:
        status = response.code
        payload = json.load(response)
    elapsed = time.perf_counter() - started
    if status == 200 and len(payload["items"]) == 20:
        return status, elapsed
    if status == 503 and payload.get("error") == "read_unavailable":
        return status, elapsed
    raise RuntimeError(f"unexpected response for user {user}: HTTP {status}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:18080")
    parser.add_argument("--mode", choices=("warm", "cold", "redis-off"), required=True)
    parser.add_argument("--concurrency", type=int, default=8)
    parser.add_argument("--requests", type=int, default=64)
    parser.add_argument("--first-user", type=int)
    args = parser.parse_args()
    url = urllib.parse.urlsplit(args.base_url)
    if url.scheme != "http" or url.hostname not in {"127.0.0.1", "localhost", "::1"}:
        parser.error("use a local HTTP benchmark API")
    if not 1 <= args.concurrency <= args.requests:
        parser.error("concurrency must be positive and no greater than requests")

    first_user = args.first_user if args.first_user is not None else (1 if args.mode == "warm" else 65)
    user_count = args.concurrency if args.mode == "warm" else args.requests
    if first_user < 1 or first_user + user_count - 1 > 256:
        parser.error("the benchmark fixture has users 1 through 256")
    users = list(range(first_user, first_user + user_count))
    base_url = args.base_url.rstrip("/")
    if args.mode == "warm":
        for user in users:
            if read_feed(base_url, user)[0] != 200:
                raise RuntimeError(f"failed to warm cache for user {user}")
    requests = [users[i % user_count] for i in range(args.requests)]

    def request(user):
        return read_feed(base_url, user)

    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as executor:
        results = list(executor.map(request, requests))
    seconds = time.perf_counter() - started
    counts = collections.Counter(status for status, _ in results)
    latencies = sorted(elapsed for status, elapsed in results if status == 200)
    p95 = 1000 * latencies[math.ceil(0.95 * len(latencies)) - 1] if latencies else None
    print(json.dumps({
        "mode": args.mode,
        "concurrency": args.concurrency,
        "requests": args.requests,
        "successful": counts[200],
        "success_per_second": round(counts[200] / seconds, 1),
        "p95_success_ms": round(p95, 1) if p95 is not None else None,
        "unavailable_percent": round(100 * counts[503] / args.requests, 1),
    }))


if __name__ == "__main__":
    main()
