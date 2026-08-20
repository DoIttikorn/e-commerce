# If you read one thing

Six services, thirty-odd packages. This is the short version: the one path that
exercises the whole system, and the handful of decisions in it that were not
obvious.

Everything below was run against the stack, not reasoned about. The numbers are
measured and the test names are real.

## The trace worth following

A buyer pays for a mug. Somebody watching a live stream, on a different machine,
sees it happen a moment later.

```mermaid
sequenceDiagram
    autonumber
    actor Buyer
    participant Order as order :8083
    participant OrderDB as (mongo-order)
    participant Relay as outbox relay
    participant Kafka as Kafka<br/>order.events
    participant Live as live :8085
    participant LiveDB as (mongo-live)
    participant Redis as Redis pub/sub
    actor Viewer

    Buyer->>Order: POST /orders/{id}/pay
    Order->>OrderDB: status → paid AND append event
    Note over Order,OrderDB: one transaction. The order and the fact<br/>that it happened commit together or not at all.
    Order-->>Buyer: 200 paid

    Note over Order,Relay: the request is finished;<br/>nothing waits for what follows

    Relay->>OrderDB: claim the oldest unpublished event
    Relay->>Kafka: publish
    Relay->>OrderDB: mark it sent

    Kafka->>Live: order.paid
    Live->>LiveDB: which live streams feature this product?
    LiveDB-->>Live: stream 6a8717…
    Live->>Redis: publish to live:stream:6a8717…
    Redis->>Live: (the instance holding the socket)
    Live->>Viewer: {"type":"purchase","product_name":"Blue Ceramic Mug",…}
```

Actual output on the socket:

```json
{"type":"purchase","stream_id":"6a87174e41287bea5092d448","viewers":1,
 "featured_product_id":"6a87171401a76356ad521cc2",
 "product_name":"Blue Ceramic Mug","quantity":2,"at":"2026-08-20T15:04:03Z"}
```

**The Order service has never heard of live streams.** No configuration names
them, no code imports them, nothing about it changed when they were built. It
publishes what it did and stops caring. The Live service decided on its own that
it was interested, and asked its own database what to do about it.

That is the whole argument for events rather than calls, and it only becomes
visible at the third consumer. The first one you could have done with an HTTP
call. The second would have been two calls. By the third the publisher is
maintaining a list of everybody who needs telling, and a failure in any of them
is a failure in checkout.

## The engineering that is actually load-bearing

### Placing an order is a saga, because a transaction is not available

Stock lives in the Product service's database. The order lives in the Order
service's. No transaction spans two MongoDB instances, so:

1. Reserve stock over gRPC — every line or none, idempotent under the caller's key
2. Write the order and its event in one local transaction
3. If step 2 fails, release the reservation

This is not a smaller version of a transaction. It is a different guarantee: at
no point is the system atomic, only eventually consistent with a compensating
action. **The window that remains is a crash between 1 and 2** — stock held
against an order that does not exist. A reaper that releases reservations with
no matching order closes it, and is not built.

Saying that out loud matters more than the code does. A saga whose failure modes
are undocumented is just a distributed bug with good intentions.

### Reserving stock is one conditional update, not a lock

```
{_id: …, stock: {$gte: n}}  →  {$inc: {stock: -n}}
```

The server matches and decrements in a single step. Nothing is read and then
written, so there is no window between the check and the change for a second
buyer to slip into. No lock, no retry loop, no contention beyond the document
itself.

Two tests hold this down, and both use real infrastructure because a fake cannot
prove concurrency:

- `TestOnlyOneBuyerGetsTheLastUnit` — twenty goroutines, one unit. Asserts
  exactly one success, nineteen refusals, and stock at zero: never negative,
  never stranded.
- `TestConcurrentOrdersCannotOversell` — twenty orders against five units,
  through two services and a real gRPC hop. Asserts exactly five orders placed.

The honest limit: a transaction wraps the multi-line case, and two transactions
touching the same product document abort and retry. On one very hot product that
becomes a throughput ceiling. A flash sale wants pre-allocated buckets or a
counter in Redis, and this is the wrong design for it.

### The transactional outbox

*Explained in full: [transactional-outbox.md](transactional-outbox.md) · [ภาษาไทย](transactional-outbox.th.md)*

Every service that writes and then publishes has a window: the write commits,
the publish fails, and nobody is told. Returning an error does not help — the
change already happened — so the event is simply lost.

Order closes it by writing the event into the same transaction as the order. A
relay publishes afterwards, and afterwards is allowed to be slow, retried and
duplicated. The trade is at-least-once delivery instead of at-most-once, which
is the right way round.

All three publishers use it — `seller`, `product` and `order`. `live` does not,
deliberately: it publishes to Redis pub/sub, and a purchase notification from
thirty seconds ago is worth nothing, so a delivery guarantee would be machinery
in service of an event that should be dropped anyway.

Verified end to end by `TestTheOutboxRelayPublishesAndMarksSent`: place an
order, subscribe a real consumer, run the relay, assert the event arrives and
the row is marked sent so it is not published forever.

The outbox is also where a distributed trace would otherwise end. The request
that produced the event has returned by the time the relay runs, so its context
is gone. `outbox.Append` therefore injects the ambient W3C trace context into
the outbox row, and the relay extracts it before publishing — a producer span
parented to the request that caused the event, seconds after that request
finished, carrying `outbox.lag_ms` to say how long it waited. Parenting to a
span that has already ended is deliberate: a trace is a causal chain, not a call
stack. `TestTheOutboxCarriesTheTraceToThePublisher` is the proof, and it is the
one test in the suite that would fail silently in production without saying so.

### Live Commerce keeps nothing in process memory

A WebSocket lives on **exactly one instance**. Everything else follows:

- **Viewer count.** A map per instance gives every viewer a number wrong by
  however many people are connected elsewhere. It lives in a Redis sorted set,
  scored by timestamp and pruned on read — because nobody sends a goodbye when a
  laptop lid closes, and an instance that crashes sends none for anybody it was
  holding.
- **Broadcast.** A purchase handled by instance B has to reach a socket held by
  instance A. Redis pub/sub carries it.
- **Reading from a socket that sends nothing.** A WebSocket close only surfaces
  through a read. A handler that never reads never learns the viewer left.

`TestABroadcastReachesASubscriberOnAnotherInstance` builds two buses on two
connections — standing in for two instances — subscribes on one and publishes on
the other. Anything less proves nothing.

### Tracing that survives the parts nobody instruments

*Details: [tech-stack.md](tech-stack.md#tracing)*

Two decisions here were not the default one.

**The HTTP middleware is hand-written instead of `otelhttp`.** The standard
contrib middleware names a span when the span starts — before the router has
matched — so the only name available is `r.URL.Path`, and
`GET /api/v1/users/68f1…` per user is unbounded cardinality in the trace
backend, where it costs indexing and money rather than a crash. The project
already solved this for Prometheus labels: capture the matched pattern, and use
it after the handler returns. A span can be renamed before `End`, so the same
trick applies. gRPC needed none of it — a method name has no IDs in it — so
`otelgrpc` is used unmodified there.

**Tracing off still propagates.** With no collector configured the SDK is
replaced by a no-op provider, but the propagator is installed regardless, so a
trace context that arrives with a request is passed on rather than dropped. A
service that swallows what it was handed does not merely fail to trace itself —
it breaks the trace of everybody upstream and downstream of it.

### Two transports, on purpose

| | Kafka | Redis pub/sub |
|---|---|---|
| Used for | orders, products, sellers | live stream events |
| Replay | yes, from any offset | none — a late subscriber sees nothing |
| Right when | the event must not be lost | the event is worthless when stale |

A purchase notification from thirty seconds ago is not worth showing. An order
event that vanishes is a support ticket. Same shape, opposite requirement.

### Two caches, opposite strategies

| | `product/rediscache` | `marketplace/rediscache` |
|---|---|---|
| Cached | one product by ID | a whole search result |
| Invalidation | on write, by exact key | none — it expires |
| Why | updating product X drops product X | which cached queries contain X is unanswerable without a second index, with its own invalidation problem |

Both fall through to MongoDB on a Redis failure. A cache that fails the request
when it is unwell has stopped being an optimisation and become a dependency.

Measured, in [../test/load/README.md](../test/load/README.md): the product cache
moves throughput by **3%** and takes MongoDB from **685,062 reads to 4**. Add a
cache to protect a dependency, not to lower latency — and measure which one you
are protecting.

## Three bugs that only running it would find

Written down because they are the argument for integration tests over confidence.

**A transaction that spins until its context expires.** Reserving stock caught
the duplicate-key error inside the transaction and returned success — which asks
the driver to commit a transaction that already contains a failed write. The
commit fails with a retryable label, `WithTransaction` retries, the duplicate is
still there. The test hung for 90 seconds. Moving the idempotency check outside
the transaction took it to 0.05s.

**A `.gitignore` line that would have shipped a repository nobody could build.**
`/api` was meant to exclude a compiled binary. It also matched the `api/`
directory holding every protobuf contract and generated file. Formatting, vet,
tests and `go build` were all green; only the Docker build, which copies the
repository as git would, failed.

**A duplicate WebSocket message.** Connecting a real viewer showed
`viewer.joined` twice — once sent directly to the new socket and once from the
broadcast it was already subscribed to. The first is now a `stream.state`
snapshot that also says what is being shown, which is more useful and not a
duplicate.

## What is missing

The original four are done. A reaper reclaims reservations nobody confirmed;
OpenTelemetry traces a request across all six services and through the outbox;
the user service holds an Ed25519 private key and everything else only the
public half; and the stock port requires a client certificate. Topics are
created with three partitions.

What is left, in the order it should be fixed:

1. **Single-node replica sets and one Kafka broker.** The last item from the
   original list, and the only one that is a deployment decision rather than
   code. The URIs name replica sets, the driver is set to `majority` reads and
   writes, and `KAFKA_PARTITIONS` is three — so adding members is compose and
   an `rs.reconfig`, not a rewrite. On a laptop they are still single points of
   failure.
2. **No dead-letter topic.** A message a consumer cannot handle is retried
   forever and blocks its partition behind it.
3. **No rate limiting or circuit breaking.** The Order → Product call has a
   timeout but no breaker, so a Product service that is slow rather than down
   is absorbed one checkout at a time.
4. **Nothing watches the outbox depth.** `outbox.PendingCount` is the number to
   page on: a stopped relay looks exactly like an idle one until it climbs.

[domains.md](domains.md) has the full map. [tech-stack.md](tech-stack.md) has why
each technology is here and what it costs.
