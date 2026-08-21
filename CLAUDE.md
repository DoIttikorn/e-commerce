# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project

An e-commerce backend in Go: one binary per domain, sharing a repository.

Six services exist — `user`, `seller`, `product`, `order`, `marketplace`,
`live` — each independently deployable, each owning **its own MongoDB
instance**. They communicate through Kafka events, with exactly one exception:
Order calls Product over gRPC to reserve stock, because a buyer cannot be told
their order exists until the stock is secured.
[docs/domains.md](docs/domains.md) is the map.

- Module path: `github.com/DoIttikorn/e-commerce`
- Go: 1.26.x (1.26.6 installed locally)
- Storage: MongoDB

The **User Management API** is the first domain and the current focus. Its
requirements are in [README-2.md](README-2.md), which also contains a second,
design-only deliverable (the Lottery Search System). Both are due before the
wider e-commerce work starts — see [Next steps](#next-steps).

Committed context:

- [Test-go.md](Test-go.md) — the role this work is aimed at. Its Responsibilities
  and Qualifications explain why the stack looks the way it does.
- [docs/highlights.md](docs/highlights.md) — the shortest useful description of
  what this system does and which parts are load-bearing. Read it before
  changing anything structural.
- [docs/](docs/) — design artefacts and technology decisions.

Local-only, not committed (see `.gitignore`):

- `NOTES.local.md` — how the current work is being assessed, and what is and is
  not credited. Read it before deciding what to build.
- `Project structure Golang.md` — the owner's own Go layout standard, which is
  reference #1 below.

## Current state

**All six domains are built, wired, and verified end to end.**

In place and passing `make lint` and `make test`:

- `internal/config` — env loading, fail-fast, reports every problem at once;
  Mongo required, Redis and Kafka optional and off by default; tested
- `internal/router` + `internal/router/chirouter` — the routing port and its chi
  adapter, with tests proving `r.PathValue` works through it
- `internal/database` — Mongo connect and reachability ping
- `internal/logging` — JSON logger that stamps the request ID on every record
- `internal/middleware` — request ID, Prometheus RED metrics, request logging
- `internal/admin` — metrics and pprof on their own port
- `internal/appserver` — the bootstrap every service shares: config, logging,
  Mongo, metrics, `/healthz` + `/readyz`, background tasks, graceful shutdown
- `internal/kafka` — publisher, consumer, idempotent topic creation
- `cmd/user`, `cmd/seller`, `cmd/product` — one thin main per service
- `Makefile`, `Dockerfile`, `docker-compose.yml`, `.github/workflows/ci.yml`,
  `.air.toml`, `.env.example`, `.gitignore`, `.dockerignore`
- `internal/user` — entity, service, `Repository` port, `mongodb` driven adapter,
  `handler` (REST) and `gapi` (gRPC) driving adapters, and the ten-second
  `CountLogger`
- `internal/seller` — shops; publishes `seller.registered` and `seller.updated`
- `internal/product` — listings; consumes seller events into a local read model,
  caches reads through a `rediscache` decorator, and serves stock reservation
  over gRPC
- `internal/order` — the saga: reserve over gRPC, write order and event in one
  transaction, compensate on failure. The only user of `internal/outbox`
- `internal/marketplace` — a search projection fed by three event streams, with
  a MongoDB text index and a TTL search cache
- `internal/live` — streams, WebSocket viewers, and Redis-backed presence and
  broadcast so both work across instances
- `internal/outbox` — transactional outbox and its relay
- `internal/auth` — bcrypt hashing, HS256 issue/verify, bearer middleware
- `api/user/v1` — `user.proto` plus generated, committed Go code
- `test/integration` — repository tests against a real MongoDB (including the
  unique index under concurrent insert) and gRPC tests over a real listener
- `test/http/api.http`, `test/grpc/requests.md` — request collections for both
  protocols, doubling as the brief's sample requests deliverable

Not written yet: `docs/lottery-search-design.md`. Known gaps in what is built
are listed in [docs/domains.md](docs/domains.md).

### Design before domain

Domain code waits on design artefacts in `docs/`: user stories, a use case
diagram, and UML sequence diagrams. Write a domain only after those exist for it,
and make the code follow them. **If a diagram and the code disagree, one of them
is wrong — say so rather than quietly implementing something else.**

**[docs/sequence-diagrams/](docs/sequence-diagrams/) is the full set** — every
endpoint of all six domains as a UML sequence diagram, 42 per language, English
and Thai (`x.md` / `x.th.md`, the same convention the outbox docs use). A diagram
and the code disagreeing means one of them is wrong, so a change to a flow
changes **both language files** with it. Validate with mermaid's own parser
rather than by eye — two blocks shipped broken because a semicolon in note text
is a statement separator in mermaid.

Done so far:

- [docs/user-domain-design.md](docs/user-domain-design.md) — the User domain:
  entity, actors, nine user stories with acceptance criteria, four sequence
  diagrams, and the decisions behind them.
- [docs/tech-stack.md](docs/tech-stack.md) — why each technology is here,
  what it costs, and the known gaps.

## Reference layouts

Three references, in **precedence order** — when they conflict, the higher one wins:

| Precedence | Reference | Used for |
|---|---|---|
| 1 | `Project structure Golang.md` (local only) | House convention: directory layout, domain package shape, file naming, logging style |
| 2 | [melkeydev/go-blueprint](https://github.com/melkeydev/go-blueprint) | Starting skeleton, Makefile target names, tooling defaults |
| 3 | [golang-standards/project-layout](https://github.com/golang-standards/project-layout) | Dictionary of top-level directory names and their meanings |

The house convention describes a multi-domain e-commerce microservice with gRPC,
Kafka, and Postgres/Mongo/Redis. That is the direction this project is heading, so
its layout now applies almost directly — but **adopt a piece only when there is
code for it.** No Kafka, no Redis, no worker binary, no Postgres today.

`project-layout` is **not** an official Go standard — its own README says so. It is
a naming dictionary, not a checklist. Do not create a directory just because that
repo lists it.

### Directories

Used:

- `/cmd` — one subdirectory per binary, named after the binary. `main.go` stays thin: load config, wire dependencies, start servers, handle shutdown.
- `/internal` — all private application logic.
- `/api` — versioned contract definitions, `api/<domain>/v1/`, per the house convention.
- `/test` — `test/integration/` for multi-component tests, `test/testdata/` for seeds and fixtures. Unit tests stay next to the code they test.
- `/docs` — design artefacts and the lottery design document.

Used when the need materialises:

- `/configs` — config files, once config outgrows env vars.
- `/pkg` — publicly reusable, **non-domain** code, per the house convention. Nothing qualifies yet. Never an overflow bucket for domain logic.

Not used: `/vendor`, `/init`, `/third_party`, `/website`, `/examples`, `/assets`,
`/githooks`, `/web`, `/build`, `/deployments`, `/scripts`. `Dockerfile` and
`docker-compose.yml` stay in the root.

## Target layout

```
.
├── api/                             # published contracts: proto and events
│   ├── user/v1/
│   │   ├── user.proto
│   │   └── user.pb.go               # generated, committed
│   └── seller/v1/events.go          # the event envelope consumers import
├── cmd/                             # one directory per service binary
│   ├── user/main.go
│   ├── seller/main.go
│   └── product/main.go              # wiring only; appserver has the rest
├── internal/
│   ├── appserver/                   # shared bootstrap for every service
│   ├── kafka/                       # publisher, consumer, topic creation
│   ├── config/                      # env-based config
│   ├── database/                    # shared driver init
│   │   ├── mongo.go                 # client construction + reachability ping
│   │   └── redis.go
│   ├── logging/                     # logger + request-ID correlation
│   ├── admin/                       # metrics + pprof, separate port
│   ├── middleware/                  # stdlib-shaped HTTP middleware
│   │   ├── requestid.go
│   │   ├── metrics.go
│   │   └── logging.go
│   ├── router/                      # HTTP framework abstraction
│   │   ├── router.go                # PORT: the Router interface
│   │   ├── pattern.go               # matched-route capture, for metrics labels
│   │   └── chirouter/               # chi adapter
│   ├── auth/                        # JWT issue/verify, password hashing, middleware
│   ├── user/                        # the template every domain copies
│   │   ├── user.go                  # entity + domain errors
│   │   ├── service.go               # business logic
│   │   ├── service_test.go          # unit tests with fakes
│   │   ├── repository.go            # PORT: Repository interface only
│   │   ├── mongodb/                 # DRIVEN adapter
│   │   ├── handler/                 # DRIVING adapter: REST
│   │   └── gapi/                    # DRIVING adapter: gRPC (CreateUser, GetUser)
│   ├── seller/                      # same shape; publishes events
│   └── product/                     # same shape, plus:
│       ├── rediscache/              # DRIVEN adapter: cache over Repository
│       └── events/                  # DRIVEN adapter: fed by seller events
├── test/{integration,testdata}/
├── docs/
├── Makefile
├── Dockerfile
├── docker-compose.yml
├── .env.example
└── README.md
```

### The hexagon

```
   handler/  (REST)  ─┐
                      ├──▶  user.Service  ──▶  user.Repository (port)  ◀── mongodb/
   gapi/     (gRPC)  ─┘         (core)
        driving adapters                            driven adapter
```

- A domain's core (`user.go`, `service.go`, `repository.go`) imports **neither**
  `net/http`, **nor** the Mongo driver, **nor** generated protobuf code. If it
  does, the abstraction has leaked.
- Two driving adapters over one service is the point: the same service serves
  REST and gRPC with no branching inside it.
- The `Repository` interface is declared by the **consumer** (the domain), and
  implemented by the adapter. Never the other way round.
- The entity carries no `bson:`, `json:`, or protobuf tags. Each adapter defines
  its own struct and maps explicitly.

Package names avoid colliding with the libraries they wrap: `mongodb` not `mongo`
(`go.mongodb.org/mongo-driver/v2/mongo`), `chirouter` not `chi`.

`internal/database/` holds connection setup and the reachability ping, nothing
else. **Queries and index definitions belong to each domain's adapter**, so adding
a domain never means editing `internal/database`.

### Adding a domain

Every domain gets the same seven-part shape as `internal/user`. Copy it exactly
rather than inventing a variation — the value of the convention is that a reader
who knows one domain knows all of them.

A domain gets `gapi/` only when something actually calls it over gRPC, and
`handler/` only when something calls it over REST. Do not create both by reflex.

### Crossing domains

Now that domains are separate processes with separate databases, these are not
style preferences — breaking one makes the services impossible to deploy apart.

- A domain never imports another domain's `mongodb`, `handler`, `gapi`, or
  service package. The only thing it may import from another domain is that
  domain's contract under `api/`.
- **No domain reads another domain's collections or database.** Compose gives
  each service its own database so this is enforced rather than agreed.
- **Do not share entities.** `product.SellerRef` is Product's own small type, not
  `seller.Seller`. A shared entity couples the two together and is the usual
  reason a "microservice" layout cannot actually be split.
- **Prefer an event to a call.** When a domain needs facts from another, it
  subscribes to that domain's events and keeps a local read model, as
  `internal/product` does with `seller_directory`. A synchronous call puts the
  other service's availability and latency on your request path.
- A synchronous call is still right when the caller needs an answer that cannot
  be stale — an authorization decision, or a balance. Then declare a narrow
  interface at the **caller** and wire the client in `main`.

### Events

Rules that come from things that have already bitten:

- **One topic per publishing domain** (`seller.events`), with a `Type` field in
  the envelope — not a topic per event type. Consumers decode the whole topic
  and ignore what does not apply, so a publisher can add an event type without
  coordinating with anybody.
- **Key by the entity the event is about.** Ordering is per partition, so two
  events about one seller must share a key or they can be applied backwards.
- **Handlers must be idempotent.** Delivery is at-least-once and the commit
  happens after the handler succeeds, so a repeat is certain, not possible.
- **Commit after handling, never before.** `internal/kafka` uses
  `FetchMessage` + `CommitMessages` for exactly this.
- **A permanently undecodable message returns nil**, not an error. Retrying it
  forever blocks its partition for every message behind it. Log it; a production
  deployment sends it to a dead-letter topic.
- **Create topics at startup** with `kafka.EnsureTopic` rather than relying on
  auto-creation. A consumer that subscribes before the first publish otherwise
  races topic creation and sits idle.
- **Tests reuse one stable consumer group**, never a unique one per run. A fresh
  group each time abandons the old one in the broker, and they accumulate.
- **Consumer lag is the metric that matters.** `make kafka-ui` shows it per
  group; a lag that climbs means the consumer is losing to the producer, and
  every health check will still be green.
- **A publisher's `BatchTimeout` must be small.** kafka-go defaults to one
  second, and the outbox relay publishes one event at a time — so the default
  put a full second on every event and capped the relay at roughly one event per
  second. Nothing but a trace showed it.
- **Never publish from a request path.** Events are written into the same
  transaction as the change that produced them, and a relay publishes them
  afterwards. A service that publishes directly can succeed at the write and
  fail at the publish, and the event is then lost with no way to detect it.
  `internal/outbox` is generic; use it. Full reasoning in
  [docs/transactional-outbox.md](docs/transactional-outbox.md).
- **The exception is an event whose value decays to nothing.** `internal/live`
  publishes to Redis pub/sub with no outbox on purpose: a delivery guarantee for
  a notification that is worthless when stale is machinery for nothing.

### Distributed writes

- **There is no transaction across services.** Each owns its own MongoDB
  instance, so anything spanning two of them is a **saga**: act, then compensate
  if a later step fails. `internal/order` is the worked example.
- **Prefer an atomic conditional update to a lock.** Reserving stock is
  `{_id, stock: {$gte: n}}` with `$inc` — the server matches and decrements in
  one step, so two buyers racing for the last unit cannot both succeed. No lock,
  no read-then-write, no contention beyond the document itself.
- **Every cross-service write takes an idempotency key.** A caller that times
  out cannot tell whether the server acted, so it will retry. The key is what
  makes the retry safe; without one, "reserve stock" becomes "reserve stock
  twice" on a bad network.
- **A compensating action must be safe to call repeatedly**, because it runs on
  the path where retries happen.
- **Never commit a transaction that already contains a failed write.** The
  commit fails with a retryable label, `WithTransaction` retries, and the call
  spins until its context expires. Handle the duplicate outside the transaction.

### Caching

- **A cache is a decorator over the `Repository` port**, never a branch inside a
  service. `internal/product/rediscache` implements `product.Repository`, so the
  service is written as though no cache exists and removing it is one line.
- **Redis failures fall through to the database.** A cache that fails the request
  when it is unavailable has become a dependency and made availability worse.
- **Redis is a readiness check, never a liveness one.**
- **Writes invalidate; they do not rewrite.** Rewriting lets two racing writers
  leave the older value cached indefinitely.
- **Invalidate exactly, never by scanning.** `RenameSeller` returns the affected
  IDs so the decorator can delete those keys; scanning Redis for keys that might
  match is `O(keyspace)`.
- **Do not cache paged lists.** Every filter and page is a separate key and every
  write would have to invalidate an unknown number of them.
- **Every cache exports a hit/miss counter.** A hit rate nobody can see is a guess.

## The router abstraction

`chi` today, `echo` possible later, with no change to any handler. The cost stays
low because it **borrows stdlib types instead of inventing new ones**.

1. **Handlers are `http.HandlerFunc`.** Never invent a custom `Context` type —
   that road ends in reimplementing binding, validation, and response writing,
   and it discards the framework's middleware ecosystem. `net/http` is the real
   lingua franca: chi uses it natively; echo wraps it with
   `echo.WrapHandler(http.Handler) echo.HandlerFunc`.
2. **Middleware is `func(http.Handler) http.Handler`** — chi's native shape, and
   echo adapts it with `echo.WrapMiddleware`.
3. **Path parameters are read with `r.PathValue("id")`**, the stdlib accessor.
   Each adapter copies its framework's params onto the request with
   `r.SetPathValue` before calling the handler. Handlers never call
   `chi.URLParam` or `c.Param`.
4. **Route patterns use `{id}`** — chi's and stdlib `ServeMux`'s form. An echo
   adapter translates `{id}` → `:id` at registration, inside the adapter.

Keep `internal/router/router.go` small — every method added is a method every
future adapter must implement:

```go
type Router interface {
    Handle(method, pattern string, h http.HandlerFunc)
    Group(prefix string, fn func(r Router))
    Use(mw ...func(http.Handler) http.Handler)
    Handler() http.Handler
}
```

The adapter tests in `internal/router/chirouter/chirouter_test.go` are written
against `router.Router`, not against chi. **Reuse them verbatim to verify any new
adapter** — that is the cheapest possible proof a swap is safe.

### Honest limits

- This works because chi, echo, gin, and stdlib are all `net/http`-based. **Fiber
  is not** — it runs on fasthttp, and bridging costs performance and correctness.
  If fiber becomes a real target, revisit this design rather than stretching it.
- Framework-specific middleware does not transfer. Write project middleware in
  the stdlib shape; use framework-provided middleware only inside the adapter.

### Realtime

Only `internal/live` has this shape, and everything in it follows from one fact:
a WebSocket lives on exactly one instance.

- **Nothing that has to be true for all viewers may live in process memory.**
  Viewer counts and broadcasts both go through Redis, or a second instance makes
  both wrong.
- **Presence must expire.** Nobody sends a goodbye when a laptop lid closes.
  Score viewers by timestamp, prune on read, and heartbeat from the socket.
- **Read from the socket even when the client sends nothing.** A WebSocket close
  only surfaces through a read; a handler that never reads never learns the
  viewer left.
- **Redis pub/sub, not Kafka, for live events.** It has no replay, which is
  correct for a feed whose value decays in seconds and wrong for anything
  durable.
- **Drop frames for a slow viewer rather than blocking.** One stalled connection
  must not hold up everyone sharing the process.

## Go conventions

- Wire through constructors (`user.NewService(repo user.Repository) *user.Service`).
  No global state, no `init()` that touches I/O.
- Interfaces are declared by the **consumer** and kept small. Do not create one
  until a second implementation or a test genuinely needs it.
- `context.Context` is the first parameter of every function that does I/O, and is
  propagated — never `context.Background()` below `main`.
- Errors: wrap with `fmt.Errorf("...: %w", err)`. Sentinel errors
  (`var ErrUserNotFound = errors.New(...)`) live in the domain package and are
  matched with `errors.Is`. **Each driving adapter owns its own error mapping** —
  `handler/` to HTTP status codes, `gapi/` to gRPC codes. The service knows about
  neither.
- Money is integer minor units (satang) plus a currency code. Never a float.
- `gofmt` on everything; `go vet ./...` clean. `make lint` checks both.

### Logging

`log/slog`, structured. Per the house convention, use `slog.LogAttrs` with typed
attributes — the variadic `...any` form boxes every value into an interface and
allocates:

```go
// SLOW — boxes "attempt" into any
slog.Info("Connecting to DB", "component", "database", "attempt", 3)

// FAST — no interface allocation
slog.LogAttrs(ctx, slog.LevelInfo, "Connecting to DB",
    slog.String("component", "database"),
    slog.Int("attempt", 3),
)
```

Always pass the request `ctx` first so trace correlation works if OpenTelemetry
is added later. Log an error once, at the boundary that handles it — never log
and also return.

## User domain requirements

From [README-2.md](README-2.md). The details that are easy to miss:

- **Email uniqueness** must be enforced by a **unique index in MongoDB**, not just
  an application-level check — the check alone races. The `mongodb` adapter owns
  both the index creation and the mapping of the duplicate-key error to
  `ErrEmailTaken`.
- **Passwords** are hashed with `bcrypt`. The hash never appears in any API
  response, gRPC message, or log line.
- **JWT** signed `HS256`, secret from config, no default. Verification must check
  the signing method matches before trusting claims.
- **JWT over gRPC** comes from request metadata, not an `Authorization` header.
  Extraction lives in each adapter; verification lives in `internal/auth`.
- **Logging middleware** captures method, path, and execution time. Already
  written, in the stdlib shape so it survives a framework swap.
- **Background goroutine** logs the total user count every 10 seconds. Takes a
  `context.Context`, exits on cancel, uses a `time.Ticker` not `time.Sleep`.
  Started in `main`, stopped on shutdown.
- **Graceful shutdown** on SIGINT/SIGTERM: stop accepting HTTP connections,
  `GracefulStop` the gRPC server, cancel the background goroutine, disconnect Mongo.
- **Input validation** on every write endpoint: required fields, email format.
  Validation failures return 400 (HTTP) / `InvalidArgument` (gRPC), never 500.
- **gRPC surface** is `CreateUser` and `GetUser` only. Do not mirror the full REST
  CRUD into gRPC: it is not credited, it doubles the maintenance, and `ListUsers`
  and `Login` are anti-patterns there — a service must never authenticate *as* a
  user, it propagates the user's token. `user.pb.go` is generated by `make proto`
  and **committed**.
- **gRPC error codes** are the adapter's own mapping, mirroring the HTTP one:
  `NotFound`, `AlreadyExists`, `InvalidArgument` (with `errdetails.BadRequest`
  field violations), `Unauthenticated`, `Internal`. The service knows none of them.
- **gRPC reflection is registered**, so grpcurl works without a local `.proto`.
  Reconsider that for a service exposed outside a trusted network.

## Observability and health

Rationale in [docs/tech-stack.md](docs/tech-stack.md). The rules that must not
be broken:

- **`/healthz` is liveness and checks nothing.** A failing Kubernetes liveness
  probe restarts the pod, so a dependency check here turns a brief Mongo blip
  into a restart loop across every instance. **Never add a dependency check to
  it.** `/readyz` is readiness and is where dependency checks belong.
- **Metrics label by route pattern, never `r.URL.Path`.** A raw path gives every
  ID its own time series and unbounded cardinality takes Prometheus down. The
  pattern arrives via `router.PatternFrom`; unmatched requests collapse to
  `unmatched`.
- **pprof and `/metrics` live on `ADMIN_ADDR`, never the API port.** pprof
  exposes process memory and can stall the process. There is a test asserting
  the API port does not serve them.
- **Correlation happens in the slog handler**, not at call sites: log with the
  request context and the `request_id` is added automatically. Never thread a
  logger through function arguments to achieve this.
- **Span names are route patterns too**, for the same cardinality reason. This
  is why `internal/middleware/tracing.go` is hand-written instead of `otelhttp`:
  the contrib middleware names the span before the router has matched, leaving
  it nothing but `r.URL.Path`. The span is renamed after `next.ServeHTTP`, which
  the OTel API permits before `End`. gRPC needs none of this — a method name has
  no IDs in it — so `otelgrpc` is used as-is via `tracing.ServerOption()` and
  `tracing.DialOption()`.
- **The propagator is installed even with tracing disabled.** No collector means
  a no-op provider and nothing exported, but a trace context that arrives with a
  request must still be passed on. A service that drops it puts a hole in
  somebody else's trace.
- **Async hops carry the trace explicitly.** `outbox.Append` injects the ambient
  trace context into the outbox row and the relay extracts it before publishing,
  so an event published a second after the request returned still belongs to
  that request. Kafka carries it in message headers, never in the payload — the
  payload is the domain's contract.
- **Sampling is `ParentBased`.** The decision is made once at the root and
  honoured downstream. Per-service sampling produces traces with missing middles.
- Only 5xx marks a span as an error. A 404 or a 422 is the server working.
- Middleware order is `RequestID` → `Tracing` → `Metrics` → `Logging`. RequestID
  must be first or the rest record uncorrelated lines, and Tracing must precede
  Logging so `trace_id` is on the context before anything logs.

## HTTP API contract

Decided in `test/http/api.http`, which holds the full request collection and
doubles as the brief's "sample API requests and responses" deliverable. Keep the
two in step: change the contract there and here together.

`postman/` carries the same contract as an importable Postman collection covering
all six services, bilingual EN/TH per request, and it is **runnable**: the whole
thing passes in the Runner. Verify with
`npx newman run postman/e-commerce.postman_collection.json -e postman/e-commerce.postman_environment.json`.
Three artefacts now describe one contract — change one, change all three.

| Method | Path | Auth |
|---|---|---|
| GET | `/healthz` | public — liveness, checks nothing |
| GET | `/readyz` | public — readiness, pings Mongo |
| POST | `/api/v1/auth/register` | public |
| POST | `/api/v1/auth/login` | public |
| GET | `/api/v1/users` | bearer |
| POST | `/api/v1/users` | bearer |
| GET | `/api/v1/users/{id}` | bearer |
| PATCH | `/api/v1/users/{id}` | bearer, self only |
| DELETE | `/api/v1/users/{id}` | bearer, self only |

**Authorization** follows option B from the design document: reads are open to
any authenticated caller, while PATCH and DELETE are restricted to the token's
own subject and answer **403** otherwise. The brief's entity has no role field,
so there is no administrator to grant wider rights to. The whole rule is
`Server.requireSelf` in `internal/user/handler/server.go` — reverting to "any
authenticated caller may act on any user" is deleting its two call sites.

Conventions:

- **JSON field names are `snake_case`** (`created_at`, `expires_at`).
- **Errors** are `{"error": "message"}`, plus a `fields` object on validation
  failures: `{"error":"validation failed","fields":{"email":"must be a valid email address"}}`.
- **PATCH, not PUT**, for updates: the brief asks to update a user's name *or*
  email, so omitted fields must be left alone.
- **409** for a duplicate email, raised from the unique index rather than a
  read-then-write check.
- **401 is identical** for an unknown email and a wrong password, so login cannot
  be used to enumerate registered addresses.
- **400 for a malformed ID, 404 for a well-formed one that does not exist.**
- A malformed JSON body is 400, never 500.

## Commands

```bash
make                        # list every target
make run                    # go run ./cmd/user
make run SERVICE=product    # user | seller | product | order | marketplace | live
make watch SERVICE=seller   # live reload one service
make build                  # every binary into ./bin

make lint                   # gofmt + vet, exactly what CI runs
make test                   # unit tests only, no infrastructure needed
make itest                  # every test, needs MongoDB + Redis + Kafka

make proto                  # regenerate api/user/v1 from the .proto

make load-smoke             # k6 sanity check, 1 user
make load-read              # k6 load on the cached catalogue read
make load-auth              # k6 load on login

make docker-run             # build and start the whole stack
make kafka-ui               # open the Kafka topic browser on :8090
make docker-logs SERVICE=product
make docker-down            # stop, keeping data volumes
make docker-clean           # stop and delete the volumes too
```

`SERVICE` defaults to `user` and accepts `user`, `seller`, or `product`.

`docker-run` passes `--remove-orphans`, which matters after a service is renamed
in `docker-compose.yml`: the old container keeps its published ports and the new
one fails to bind.

`make test` passes `-short`, so integration tests must guard on
`testing.Short()` to stay out of it. `make itest` drops the flag and expects
MongoDB, Redis and Kafka to be reachable; with the compose stack up it needs no
environment variables, because the integration tests default to `127.0.0.1`.

CI runs both as separate jobs, plus an image build per service.

## Testing

- Go's standard `testing` package. No mocking framework; hand-write fakes.
- **Load tests live in `test/load/`** and run with k6 against a live stack.
  [test/load/README.md](test/load/README.md) carries the measured numbers,
  including the one worth knowing: the Redis cache moves throughput by 3% and
  takes MongoDB from 685,062 reads to 4. Add a cache to protect a dependency,
  not to lower latency — and measure which one you are protecting.
- Load-test thresholds are **regression alarms, not targets**: `p(95)<150` on a
  path that runs at 7.56 ms exists so a tenfold slowdown fails the build.
- **`service_test.go`** sits next to `service.go` and uses a fake `Repository`
  defined in the test file. This is the payoff of the port interface: the whole
  business-logic suite runs with no database and no HTTP.
- **Handler tests** use `httptest` against `Router.Handler()`, covering
  auth-rejection paths (missing token, bad signature, expired) as well as
  happy paths.
- **gapi tests** call the server methods directly with a fake service.
- **Integration tests** live in `test/integration/`, guarded by
  `testing.Short()`, and run against a real MongoDB via `make itest`. Unique-index
  behaviour must be covered here — a fake cannot prove it.
- Table-driven where there is more than one case. Test behaviour, not
  implementation: a test that breaks on a refactor which preserved behaviour is a
  bad test.

## Configuration

- Env-based config in `internal/config`, loaded once in `main`. Nothing else calls
  `os.Getenv`.
- Settings are grouped into `Mongo`, `Redis`, and `Kafka` structs rather than a
  flat list. Put a new backing service in its own struct.
- `.env` is local-only and gitignored; `.env.example` is the committed template
  and must list every variable.
- Fail fast at startup on a missing or invalid required variable, and report every
  problem at once. No defaults for the JWT secret or the Mongo URI.

### Required versus optional infrastructure

Mongo is required. **Redis and Kafka are optional and default to off**, because
their config exists ahead of the domains that will use them. `Redis.Enabled()`
and `Kafka.Enabled()` report whether an endpoint was configured; an unset
`REDIS_ADDR` or `KAFKA_BROKERS` leaves the feature off rather than failing
startup.

When wiring a client for either, branch on `Enabled()` — never assume it is
present. A domain that genuinely requires one should say so at startup rather
than failing on first use.

`KAFKA_GROUP_ID` is required whenever `KAFKA_BROKERS` is set and is deliberately
not defaulted: two deployments sharing an accidental consumer group would
silently steal each other's messages.

Locally, Redis and Kafka run behind the compose `full` profile so the default
stack stays fast. Kafka advertises two listeners — `kafka:9092` inside the compose
network, `localhost:29092` from the host — which is the usual thing to get wrong.

## Lottery Search System

A design document only — **do not write implementation code for it.** It goes in
`docs/lottery-search-design.md`. Requirements are in [README-2.md](README-2.md);
`NOTES.local.md` explains why this document carries more weight than its
"no code required" framing suggests, and lists the angles worth covering.

## E-commerce roadmap

All six are built. [docs/domains.md](docs/domains.md) has the map, the
reasoning, and the known gaps.

Each arrives as a full domain package per [Adding a domain](#adding-a-domain).

Redis and Kafka are wired, in `internal/database/redis.go` and `internal/kafka/`
per the house convention. Both remain optional to the platform: a service that
genuinely needs one says so at startup rather than failing on first use.

Note the difference between the two uses of Redis. In `product` and
`marketplace` it is a cache and genuinely optional — unset `REDIS_ADDR` and they
run identically, only slower. In `live` it is **required**, because presence and
broadcast are shared state rather than a cache: two instances without it would
each have their own idea of the audience and both would be wrong.

`docker-compose.ha.yml` is an opt-in overlay giving Product a **three-member**
set. It exists to prove the claim that multi-node needs no code change — kill
the primary and the service keeps serving — and it is opt-in because eighteen
mongod processes is the wrong default for a laptop. `make docker-run-ha`.

**Certificate generation must stay idempotent.** `servicetls.Generate` leaves an
existing, unexpired set alone. Minting a fresh CA invalidates everything the old
one signed, so an init container that regenerates on every `up` breaks mutual
TLS for any service that did not happen to restart with it.

Every MongoDB instance is a **single-node replica set**, which transactions and
change streams both require. From inside the compose network use the set name;
from the host use `directConnection=true`, because each set advertises its
member as `mongo-<service>:27017` — a name that resolves in the network and
nowhere else.

## Working notes for Claude

- Do not create empty directories or placeholder files. Add structure when there
  is code for it.
- Prefer the standard library. Before adding a dependency, say what it does that
  stdlib cannot.
- Run `make lint` and `make test` after changes. Report failures with actual
  output, not a summary.
- Read `NOTES.local.md` before deciding scope. The graded deliverables come first;
  e-commerce breadth comes after.

## Dependencies

Deliberately few. Config parsing, JSON, HTTP, and logging need no third party.

- `github.com/go-chi/chi/v5` — router, reachable only from `internal/router/chirouter`
- `go.mongodb.org/mongo-driver/v2` — **v2, not v1.** The API differs: `mongo.Connect`
  takes options only, no `context.Context`, and it does not contact the server, so
  a `Ping` is what turns an unreachable database into a startup failure. Write and
  read concerns are set to `majority` in `internal/database`, so the code is
  already correct on a multi-node set and costs nothing on a single-node one.
- `github.com/prometheus/client_golang` — metrics. Collectors are registered on
  an explicit `prometheus.NewRegistry()` built in `main`, never the package-level
  default, so there is no global state and tests can use a throwaway registry.
- `github.com/redis/go-redis/v9` — caching, search cache, presence. Always
  optional: a domain using it must run without it.
- `github.com/segmentio/kafka-go` — events. Reachable only from `internal/kafka`.
- `github.com/golang-jwt/jwt/v5` — tokens. Verification pins the algorithm with
  `WithValidMethods`; without that, a token signed with the *public* key as an
  HMAC secret verifies.
- `golang.org/x/crypto/bcrypt` — password hashing.
- `google.golang.org/grpc` — internal RPC.
- `github.com/coder/websocket` — live commerce. `gorilla/websocket` is archived.
- `go.opentelemetry.io/otel` + `sdk` + the OTLP exporter + `otelgrpc` — tracing.
  The API (`otel/trace`) is imported by `internal/logging`; the SDK is imported
  only by `internal/tracing`, so nothing else can install a provider.

## Local tooling

Available: Go 1.26.6, Docker, `air`, `migrate`.
Not installed — install on demand: `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc`,
`golangci-lint`, `go-blueprint`.

## Next steps

Six domains are built — User, Seller, Product, Order, Marketplace, Live Commerce
— each with a separate MongoDB instance, plus the transactional outbox, mutual
TLS on the internal gRPC link, asymmetric JWT, and OpenTelemetry tracing across
all of it. `README.md` is written and verified end to end. What remains:

1. `docs/lottery-search-design.md` — see `NOTES.local.md` for why this carries
   more weight than its "no code required" framing suggests.
2. The gaps listed in [docs/highlights.md](docs/highlights.md): multi-node
   replica sets, a dead-letter topic, rate limiting and circuit breaking, and an
   alert on outbox depth.
