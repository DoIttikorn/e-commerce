# Load tests

"Handles high traffic" is a claim until it is measured. These are the
measurements, taken with [k6](https://k6.io).

```bash
brew install k6
make docker-run        # the stack must be up
make load-smoke        # 1 VU, proves the wiring
make load-read         # the cached catalogue read
make load-auth         # login, for contrast
```

Each script seeds its own data through the real API — register an account, open
a shop, wait for the seller event to reach the product service, list a product —
so nothing is measured against a fixture that could not exist in production.

## Results

Taken on an M-series MacBook Air with the full compose stack (three services,
MongoDB, Redis, Kafka) sharing one machine with k6 itself. Absolute numbers on a
laptop mean little; the **ratios and the shapes** are what to read.

| | `GET /products/{id}` | `POST /auth/login` |
|---|---|---|
| Throughput | **13,818 req/s** | **63 req/s** |
| p95 | 7.56 ms | 257 ms |
| p99 | 13.0 ms | — |
| Peak VUs | 100 | 20 |
| Failures | 0.0001% | 0% |

The two differ by **220x**, and that is not a defect. Login is CPU-bound on
bcrypt, which is expensive on purpose: the cost is what makes an offline attack
on a stolen hash impractical. Lowering it would improve this number and weaken
the system. This is also the argument for the token — pay the cost once at
login, and every subsequent request verifies an HMAC instead.

Judging both endpoints against one latency threshold would be measuring the
wrong thing, which is why `product_read.js` sets `p(95)<150` and `auth.js` sets
`p(95)<2000`.

## What the cache is actually worth

The same read test was run twice: once against the normal product service, and
once against an instance started without `REDIS_ADDR`, so the decorator was
never wired.

| | With Redis | Without Redis |
|---|---|---|
| Throughput | 13,818 req/s | 13,377 req/s |
| p95 | 7.56 ms | 8.55 ms |
| **Reads reaching MongoDB** | **4** | **685,062** |

**The cache barely moved latency or throughput — a 3% difference — and that is
the honest result.** MongoDB is on the same host, and the query is a single
indexed `_id` lookup, which is the cheapest thing it can do. The bottleneck was
never the database, so removing database calls did not speed anything up.

What the cache did do is take **685,062 queries down to 4**, confirmed
independently by MongoDB's own `opcounters.query`. That is the number worth
having, and it is a capacity argument rather than a latency one:

- A database serving 13k queries per second for one hot product has no capacity
  left for the queries that cannot be cached.
- The gap widens the moment the database stops being a process on the same
  machine. Across a network, or on a managed instance, each of those 685,062
  round trips costs milliseconds rather than microseconds.
- It is also a blast-radius argument: with the cache, a traffic spike on a
  trending product is absorbed by Redis instead of arriving at the primary.

Two conclusions that only fall out of having measured:

1. **Do not add a cache expecting latency.** Add it to protect a dependency, and
   confirm which one you are protecting.
2. **These thresholds are regression alarms, not targets.** `p(95)<150` passes
   at 7.56 ms with enormous headroom; it exists so that a change which makes the
   read path ten times slower fails the build rather than being noticed in
   production.

## Reproducing the comparison

```bash
# Start a second product service with no Redis, sharing the same MongoDB.
JWT_SECRET=$(grep '^JWT_SECRET=' .env | cut -d= -f2-) \
HTTP_ADDR=:8083 ADMIN_ADDR=:6063 GRPC_ADDR=:9099 \
MONGO_URI=mongodb://127.0.0.1:27017/?directConnection=true MONGO_DATABASE=ecommerce_product \
KAFKA_BROKERS=127.0.0.1:29092 KAFKA_GROUP_ID=nocache-experiment \
go run ./cmd/product

# Point the same script at it.
k6 run -e PRODUCT_URL=http://127.0.0.1:8083 -e PRODUCT_ADMIN_URL=http://127.0.0.1:6063 \
  test/load/product_read.js
```

`product_read.js` reads `product_cache_lookups_total` from the admin port before
and after the run and prints the hit rate, so the comparison needs no extra
tooling. A run against the cache-less instance reports no lookups at all, which
is how you know the decorator really was absent rather than merely cold.

## What these tests do not cover

Stated so nobody reads more into the numbers than is there:

- **Write load.** Every scenario is read-heavy. Sustained product creation would
  exercise Kafka, the unique indexes, and cache invalidation together.
- **A cold cache under load.** Every measured run starts warm. The interesting
  failure is a thundering herd when a popular key expires and every request
  misses at once — single-flight, not a bigger cache, is the answer.
- **Realistic key distribution.** All traffic hits one product ID, which is the
  best case for a cache. A Zipf distribution over thousands of products would
  give a hit rate below 99.9997%.
- **Failure injection.** The behaviour worth testing is Redis disappearing
  mid-run: the code falls through to MongoDB by design, and that path has unit
  coverage but no load coverage.
- **More than one machine.** k6, all three services, and every backing store
  shared one laptop, so they were competing for the same cores.
