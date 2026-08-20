# Technology choices

Why each piece of the stack is here, and what it is meant to buy. Written to be
argued with: where a choice has a real cost, the cost is stated.

## Transport: REST *and* gRPC, not one or the other

The two are not competing options — they serve different callers.

| | REST | gRPC |
|---|---|---|
| Direction | north-south: client to system | east-west: service to service |
| Callers | browsers, mobile apps, partners, curl | other services |
| Encoding | JSON over HTTP/1.1 | protobuf over HTTP/2 |

REST at the edge, because **a browser cannot speak gRPC natively** — it needs
grpc-web plus a translating proxy, which is infrastructure to run and debug for
no gain at the edge. REST is also inspectable with curl, cacheable by ordinary
proxies, and needs no code generation from whoever consumes it. When something
is wrong in production at 3am, being able to replay a request by hand matters.

gRPC between services, because that is where the volume is. Internal fan-out
usually dwarfs external traffic, and there protobuf's smaller payloads and
cheaper encoding, HTTP/2 connection multiplexing, native streaming, and a
contract enforced at compile time all pay for themselves.

The hexagonal layout makes this concrete rather than aspirational: `handler/`
and `gapi/` are two driving adapters over one service, so the same business
logic serves both without a branch anywhere inside it.

### chi behind a port

`chi` is the router, reached only through `internal/router`. Handlers are plain
`http.HandlerFunc` and read parameters with `r.PathValue`, so no handler imports
a framework and swapping to echo is one new adapter file.

The cost is one indirection layer. It is small because the port borrows standard
library types rather than inventing a `Context` of its own — the trap that turns
this kind of abstraction into a reimplementation of the framework. The limit is
that it only works for `net/http`-based frameworks; fiber runs on fasthttp and
would need a different design, not a stretched version of this one.

## Storage

**MongoDB** is required by the brief. The document model suits a catalogue where
products carry different attributes per category, and it removes the migration
step that a relational schema would need for every such change.

Two constraints worth stating before they bite:

- **Multi-document transactions need a replica set.** The single standalone node
  in `docker-compose.yml` cannot do them. This does not affect the User domain,
  which writes one document at a time, but Order will need atomicity across an
  order and its stock decrement — so compose must move to a single-node replica
  set before that work starts.
- Uniqueness must be a database index, not an application check. A read-then-write
  check races under concurrent registration; the unique index is the only thing
  that actually holds.

**Redis** is configured for caching, distributed locking, rate limiting, and
idempotency keys. Its most interesting use here is allocation: `SPOP` removes and
returns a set member atomically, so two concurrent buyers cannot be handed the
same last item — no lock required. Knowing where an atomic primitive removes the
need for a lock is most of what "handles high concurrency" means in practice.

**Kafka** is configured for asynchronous workflows: order state changes, audit
trails, and anything a request should not wait for. It buys decoupling, replay
after a consumer bug, and consumer-group scaling.

Neither Redis nor Kafka has a Go client yet, on purpose. Both are wired when a
domain needs them; a dependency with no caller is worse than no dependency.

## Observability

The job description asks for performance tuning, monitoring, troubleshooting,
and production issue analysis. Those are the things logs alone cannot give you.

| Concern | Tool | Where |
|---|---|---|
| Structured logs | `log/slog`, JSON | `internal/logging` |
| Correlation | request ID, propagated from `X-Request-ID` | `internal/middleware` |
| Tracing | OpenTelemetry, OTLP to Jaeger | `internal/tracing`, `internal/middleware` |
| Metrics | Prometheus, RED signals + Go runtime | `internal/middleware`, `internal/admin` |
| Profiling | `net/http/pprof` | `internal/admin` |

**Correlation is done in the slog handler, not at call sites.** A custom
`slog.Handler` reads the request ID from the context and stamps it on every
record, so any code that logs with the request context is correlated for free —
including domain code that knows nothing about HTTP.

**Metrics are labelled by route pattern, never by URL path.** `/users/{id}`
labelled by path would mint one time series per user ID; unbounded label
cardinality is the usual way a monitored service takes down the monitoring
system. Requests matching no route collapse into a single `unmatched` series so
that a scanner probing random URLs cannot do the same thing.

**pprof and metrics are on a separate port** (`ADMIN_ADDR`, default `:6060`),
never the public API port. pprof exposes process memory and lets a caller stall
the process for the duration of a profile, so it must not be reachable from the
internet. The API port returns 404 for both paths.

### Tracing

A request ID answers "what else happened while serving this request" — inside
one process. With six services it stops there. It does not survive a gRPC call,
and it certainly does not survive an event written to an outbox during a request
and published to Kafka a second after that request returned. "Checkout was slow"
stays answerable exactly one hop deep.

A trace is one ID propagated across all of it, in W3C Trace Context format —
`traceparent` on HTTP, gRPC metadata, a Kafka message header. Every log line
carries `trace_id` and `span_id`, added in the same slog handler that adds the
request ID, so finding a bad line and opening the whole distributed request are
one action apart.

**Tracing is optional, on the same terms as Redis.** With
`OTEL_EXPORTER_OTLP_ENDPOINT` unset the service installs a no-op provider and
exports nothing — but it still installs the propagator, so a trace context that
arrives with a request is passed on rather than dropped. A service that drops
what it was handed puts a hole in a trace that everybody else is filling in.

**The HTTP middleware is hand-written rather than `otelhttp`,** for the same
reason the metrics middleware labels by route pattern. `otelhttp` names a span
when the span starts, which is before chi has matched a route, so the only name
available is `r.URL.Path` — and `GET /api/v1/users/68f1…` per user is the trace
backend's version of unbounded Prometheus cardinality, where it costs indexing,
grouping, and money. `internal/middleware/tracing.go` captures the pattern and
renames the span on the way out, which the OpenTelemetry API allows before
`End`. gRPC needs no such treatment: a method name contains no IDs, so the
contrib stats handler is used as-is.

**Sampling is `ParentBased`.** The decision is taken once at the root and
honoured everywhere downstream. Sampling independently per service is the
standard way to end up with traces that are missing their middle.

**The interesting hop is the outbox.** An event is written inside the same
transaction as the change that produced it and published later by a background
relay — by which time the producing request is long gone. So `outbox.Append`
injects the ambient trace context into the outbox row, and the relay extracts it
before publishing, opening a producer span parented to the request that caused
the event. Its `outbox.lag_ms` attribute is how long the event actually waited.
The Kafka consumer then extracts the context from the message headers, so a
seller rename and the product rows it eventually rewrites are one trace.

Parenting to a span that has already ended is deliberate. The alternative — a
span link — keeps producer and consumer in separate traces joined by reference,
which is the better shape when one message fans in from many producers. Here
each event has exactly one cause, and being able to open a checkout and see
what it published is the entire reason for having this.

Locally, `docker compose up` runs Jaeger and the traces are at
<http://localhost:16686>.

## Availability

**Liveness and readiness are separate endpoints, and the difference matters.**

- `GET /healthz` — liveness. Returns 200 whenever the process is running, and
  checks no dependencies at all.
- `GET /readyz` — readiness. Pings MongoDB with a 2 second timeout.

A failing Kubernetes liveness probe *restarts the pod*. Checking the database
from a liveness endpoint therefore converts a brief MongoDB blip into a restart
loop across every instance simultaneously, turning a degraded service into an
outage. A failing readiness probe instead removes the instance from the load
balancer and leaves it running, so it rejoins by itself once the dependency is
back. This was verified by stopping MongoDB: liveness stayed 200, readiness
returned 503, and readiness recovered on its own when MongoDB returned.

**Graceful shutdown** on SIGINT/SIGTERM drains in-flight requests within
`SHUTDOWN_TIMEOUT`, then disconnects MongoDB. The API server drains before the
admin server, so the final moments of traffic are still measurable.

**Fail-fast configuration.** Every problem is reported at once at startup, and
there is no default for the JWT secret or the MongoDB URI. A service that starts
with a guessed security parameter is worse than one that refuses to start.

**Write and read concerns are set explicitly, not left to the driver.** Writes
are `majority`, so a failover cannot roll back an acknowledged write; reads are
`majority` from the primary, so nothing is returned that an election could
erase. On the single-node sets compose runs, `majority` is identical to `w:1`
and costs nothing — which is exactly why it is set now. The code is already
correct for a three-member set; adding members is a compose change and an
`rs.reconfig`, not a code change somebody has to remember.

## Build and delivery

- **Multi-stage Docker build** onto `distroless/static`: no shell, no package
  manager, non-root by default, and a smaller attack surface than an Alpine base.
- **GitHub Actions** runs formatting, vet, unit tests with `-race`, integration
  tests against a real MongoDB service container, and an image build.
- **Makefile** is the single entry point, so what passes locally is what CI runs.

## Dependencies

Deliberately few — configuration, JSON, HTTP, and logging are all standard
library here.

| Module | For |
|---|---|
| `github.com/go-chi/chi/v5` | routing, behind `internal/router` |
| `go.mongodb.org/mongo-driver/v2` | MongoDB |
| `github.com/prometheus/client_golang` | metrics |
| `github.com/redis/go-redis/v9` | caching, search cache, presence |
| `github.com/segmentio/kafka-go` | events between services |
| `github.com/golang-jwt/jwt/v5` | token issue and verification |
| `github.com/coder/websocket` | live commerce chat |
| `google.golang.org/grpc` | internal RPC |
| `go.opentelemetry.io/otel` + SDK, OTLP exporter, `otelgrpc` | tracing |

## Known gaps

Honest list, roughly in priority order:

1. **Single-node replica sets, and one Kafka broker.** Every MongoDB instance in
   `docker-compose.yml` is a one-member set, which gives transactions and change
   streams but no redundancy: lose the node and lose the service. Kafka is one
   broker with `replicationFactor: 1`, same story. Both are deployment
   decisions, not code ones — the connection strings already name replica sets,
   the driver is configured for `majority` writes and reads so nothing changes
   when members are added, and `KAFKA_PARTITIONS` already sets three partitions
   per topic. See [Availability](#availability).
2. **No rate limiting or circuit breaking.** Redis is wired and is where a
   distributed limiter belongs; the Order → Product call has a timeout but no
   breaker, so a slow Product service is absorbed one checkout at a time.
3. **No dead-letter topic.** A message a consumer cannot handle is retried
   forever and blocks its partition. A retry budget and a DLQ is the fix.
4. **Traces are not sampled at production rates.** `OTEL_TRACES_SAMPLER_ARG` is
   `1.0` in compose, which is right for a laptop and the first number to turn
   down under real traffic.
5. **No alerting.** The metrics exist and `outbox.PendingCount` is the number to
   page on — a stopped relay looks exactly like an idle one until it climbs —
   but nothing is watching them.
