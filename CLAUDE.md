# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project

Backend Golang coding test, submitted as part of a job application.

Three documents in this repo define the work. Read all three before starting:

| File | What it is |
|---|---|
| [Test-go.md](Test-go.md) | **The job description.** Why this test exists and what the reviewer is looking for |
| [README-2.md](README-2.md) | **The brief.** Scope, requirements, and grading criteria for the submission |
| [Project structure Golang.md](Project%20structure%20Golang.md) | **House convention.** The owner's own Go layout standard |

Two deliverables:

1. **User Management API** — REST API in Go, MongoDB for persistence, JWT auth.
   Code implementation.
2. **Lottery Search System** — wildcard search over 10M six-digit tickets with a
   no-duplicate-allocation constraint. **Design document only, no code.**

- Module path: `github.com/DoIttikorn/user-management-api`
- Go: 1.26.x (1.26.6 installed locally)

## Current state

**The skeleton is built and green; the `user` domain is not written yet.**

In place and passing `go vet`, `gofmt`, and `go test -short -race ./...`:

- `internal/config` — env loading, fail-fast, reports every problem at once
- `internal/router` + `internal/router/chirouter` — the routing port and its chi
  adapter, with tests proving `r.PathValue` works through it
- `internal/database` — Mongo connect and reachability ping
- `internal/middleware` — request logging
- `cmd/api/main.go` — wiring, `/healthz`, graceful shutdown
- `Makefile`, `Dockerfile`, `docker-compose.yml`, `.github/workflows/ci.yml`,
  `.air.toml`, `.env.example`, `.gitignore`, `.dockerignore`

Not written yet, by decision: everything under `internal/user`, `internal/auth`,
and `api/user/v1`.

### Design before domain

The domain is deliberately not implemented yet. The next step is design
artefacts, which go in `docs/`:

- user stories
- use case diagram
- UML sequence diagrams

Write the domain only after those exist, and make the code follow them. If a
sequence diagram and the code disagree, one of them is wrong — say so rather
than quietly implementing something else.

## The role being hired for

[Test-go.md](Test-go.md) is a **Senior Software Engineer (Backend – Golang & gRPC)**
posting. This is the single most useful context in the repo, because the coding
test is a proxy for the job: the reviewer is checking whether the candidate can do
what the Responsibilities list describes.

Consequences that change how effort is spent:

- **gRPC is a core job requirement, not a bonus.** The brief files it under
  "optional", but the job title contains it and Qualifications list "Hands-on
  experience with gRPC and REST APIs". Doing it is not optional in practice.
- **Docker and CI/CD are explicit Qualifications.** Both appear in the brief's
  bonus list. Do both.
- **Redis, Kafka, distributed systems, and high concurrency are Qualifications
  that the CRUD API cannot demonstrate.** A five-endpoint user API has no honest
  place for Redis caching or a message queue. The lottery design document is
  where these get demonstrated — see below. This is why deliverable 2 carries
  far more weight than its "no code required" framing suggests.
- **The folder is named `e-commerce` on purpose.** The Responsibilities list
  "Marketplace, Seller, User, Product, Order, and Live Commerce domains", and
  Nice to Have is e-commerce/marketplace experience. The house convention's
  `seller/` and `customer/` domains come from the same place. The folder name is
  the employer's domain, not a mistake — but the submitted repo should still be
  named for what it actually contains.

### Where each qualification gets demonstrated

| Qualification (from the JD) | Demonstrated by |
|---|---|
| Golang proficiency | The whole submission; idiomatic Go is an explicit grading criterion |
| gRPC | `internal/user/gapi/` + `api/user/v1/` |
| REST APIs | `internal/user/handler/` |
| MongoDB | `internal/user/mongodb/`, unique index, driver abstraction |
| Microservices architecture | House-convention layout; ports and adapters |
| Docker | `Dockerfile` + `docker-compose.yml` |
| CI/CD | GitHub Actions workflow (go-blueprint's `githubaction` feature) |
| **Redis (caching + distributed locking)** | **Lottery design doc** |
| **Kafka / message queues** | **Lottery design doc**, if it earns its place |
| **Distributed systems, performance, high concurrency** | **Lottery design doc** |
| Code review / technical design | The "documented assumptions and design decisions" deliverable |

Do **not** bolt Redis or Kafka onto the user API to tick boxes. The brief's
grading criteria reward "code quality, structure, and readability"; unrequested
infrastructure in a CRUD service reads as over-engineering and costs more than it
gains. Show that knowledge where it was actually asked for.

### Decisions taken

| Decision | Choice |
|---|---|
| HTTP framework | `chi`, behind a router abstraction (see below) |
| Hexagonal bonus | **Yes** — Mongo implementation in a driven-adapter subpackage |
| gRPC bonus | **Yes** — job requirement, not optional |
| Docker + CI/CD | **Yes** — explicit JD qualifications |
| Redis / Kafka in the API code | **No** — design doc only |
| Database | MongoDB (required by the brief) |

## Reference layouts

Three references, in **precedence order** — when they conflict, the higher one wins:

| Precedence | Reference | Used for |
|---|---|---|
| 1 | [Project structure Golang.md](Project%20structure%20Golang.md) | House convention: directory layout, domain package shape, file naming, logging style |
| 2 | [melkeydev/go-blueprint](https://github.com/melkeydev/go-blueprint) | Starting skeleton, Makefile target names, tooling defaults |
| 3 | [golang-standards/project-layout](https://github.com/golang-standards/project-layout) | Dictionary of top-level directory names and their meanings |

The house convention describes a larger microservice (gRPC, Kafka,
Postgres/Mongo/Redis, multiple domains) — it was written against this same job
description. This project is a subset of it. **Follow its conventions; do not
adopt the parts this project has no need for** — no Kafka, no worker binary, no
Postgres, one domain instead of two.

`project-layout` is **not** an official Go standard — its own README says so. It
is a naming dictionary, not a checklist. Do not create a directory just because
that repo lists it; at this size an over-scaffolded tree counts against "code
quality, structure, and readability", not for it.

### Directories, and whether this project uses them

Used:

- `/cmd` — one subdirectory per binary, named after the binary. `main.go` stays thin: load config, wire dependencies, start servers, handle shutdown.
- `/internal` — all private application logic.
- `/api` — versioned contract definitions, `api/user/v1/`, per the house convention.
- `/test` — `test/integration/` for multi-component tests, `test/testdata/` for seeds and fixtures. Unit tests stay next to the code they test.
- `/docs` — the lottery design document (a graded deliverable).

Used only if the need materialises:

- `/configs` — environment config files, once config outgrows env vars.
- `/pkg` — publicly reusable, **non-domain** code, per the house convention. Nothing qualifies yet. Do not use it as an overflow bucket for domain logic.

Not used here:

- `/vendor`, `/init`, `/third_party`, `/website`, `/examples`, `/assets`, `/githooks`, `/web`, `/build`, `/deployments`, `/scripts` — no anticipated need at this size. `Dockerfile` and `docker-compose.yml` stay in the root.

## Target layout

```
.
├── .github/workflows/
│   └── ci.yml                       # build + vet + test  (JD: CI/CD)
├── api/
│   └── user/
│       └── v1/
│           ├── user.proto           # CreateUser + GetUser
│           └── user.pb.go           # generated, committed
├── cmd/
│   └── api/
│       └── main.go                  # wiring, starts HTTP + gRPC, graceful shutdown
├── internal/
│   ├── config/                      # env-based config
│   ├── database/                    # shared driver init
│   │   └── mongo.go                 # client construction + reachability ping
│   ├── middleware/                  # stdlib-shaped HTTP middleware
│   │   └── logging.go
│   ├── router/                      # HTTP framework abstraction
│   │   ├── router.go                # PORT: the Router interface
│   │   └── chirouter/               # chi adapter (the only one, for now)
│   │       └── chirouter.go
│   ├── user/                        # User domain
│   │   ├── user.go                  # entity + domain errors
│   │   ├── service.go               # core business logic
│   │   ├── service_test.go          # unit tests with fakes
│   │   ├── repository.go            # PORT: Repository interface only
│   │   ├── mongodb/                 # DRIVEN adapter: implements user.Repository
│   │   │   └── repository.go
│   │   ├── handler/                 # DRIVING adapter: REST
│   │   │   ├── server.go            # handler struct + route registration
│   │   │   └── http_create_user.go  # one file per endpoint
│   │   └── gapi/                    # DRIVING adapter: gRPC
│   │       ├── server.go            # gRPC server struct
│   │       ├── rpc_create_user.go
│   │       └── rpc_get_user.go
│   └── auth/                        # JWT issue/verify, password hashing, middleware
├── test/
│   ├── integration/
│   └── testdata/
├── docs/
│   └── lottery-search-design.md     # deliverable 2
├── Makefile
├── docker-compose.yml               # api + mongo
├── Dockerfile
├── .env.example
└── README.md                        # setup, JWT guide, sample requests, decisions
```

### The hexagon

This is the whole point of the structure, so it is worth stating plainly:

```
   handler/  (REST)  ─┐
                      ├──▶  user.Service  ──▶  user.Repository (port)  ◀── mongodb/
   gapi/     (gRPC)  ─┘         (core)
        driving adapters                            driven adapter
```

- `internal/user` (the core: `user.go`, `service.go`, `repository.go`) imports
  **neither** `net/http`, **nor** the Mongo driver, **nor** any generated protobuf
  code. If it does, the abstraction has leaked and the bonus is not earned.
- Two driving adapters over one service is the clearest possible demonstration of
  ports and adapters — the same `user.Service` serves REST and gRPC with no
  branching inside it. It also directly answers the JD's "gRPC **and** REST APIs".
- The `Repository` interface is declared by the **consumer** (`internal/user`),
  and implemented by `internal/user/mongodb`. Never the other way round.
- The entity carries no `bson:`, `json:`, or protobuf tags. Each adapter defines
  its own struct and maps explicitly.

The package is `internal/user/mongodb/`, not `mongo/`, to avoid colliding with the
imported driver package `go.mongodb.org/mongo-driver/v2/mongo`. The same reason
gives `internal/router/chirouter/` its name.

`internal/database/` holds connection setup and the reachability ping, nothing
else. **Queries and index definitions belong to the domain's own adapter**, so
adding a domain never means editing `internal/database`. The `mongodb` adapter
creates the indexes it depends on.

## The router abstraction

`chi` today, `echo` possible later, with no change to any handler. This adds a
layer, and the way to keep the cost low is to **borrow stdlib types instead of
inventing new ones**.

### Rules

1. **Handlers are `http.HandlerFunc`.** Do not invent a custom `Context` type.
   That road ends in reimplementing binding, validation, and response writing,
   and it throws away the framework's middleware ecosystem. `net/http` is the
   real lingua franca: chi uses it natively, and echo and gin both ship wrappers
   (`echo.WrapHandler(http.Handler) echo.HandlerFunc`).
2. **Middleware is `func(http.Handler) http.Handler`.** chi's native shape;
   echo adapts it with `echo.WrapMiddleware`.
3. **Path parameters are read with `r.PathValue("id")`** — the stdlib accessor,
   available since Go 1.22. Each adapter copies its framework's params into the
   request via `r.SetPathValue(name, value)` before calling the handler. Handlers
   never call `chi.URLParam` or `c.Param`.
4. **Route patterns use `{id}` syntax** — chi's and stdlib `ServeMux`'s form. An
   echo adapter must translate `{id}` → `:id` when registering. Write this
   translation in the adapter, never in the route table.

The payoff: every file under `handler/` imports `net/http` and nothing else
framework-related. Swapping to echo means writing one new adapter file and
changing one line in `main.go`.

### The port

`internal/router/router.go` stays small. Resist growing it — every method added
is a method every future adapter must implement:

```go
type Router interface {
    Handle(method, pattern string, h http.HandlerFunc)
    Group(prefix string, fn func(r Router))
    Use(mw ...func(http.Handler) http.Handler)
    Handler() http.Handler   // for http.Server and httptest
}
```

### Honest limits

- This works because chi, echo, gin, and stdlib are all `net/http`-based. **Fiber
  is not** — it runs on fasthttp, and bridging costs both performance and
  correctness. If fiber ever becomes a real target, this abstraction is the wrong
  design and should be revisited rather than stretched.
- Framework-specific middleware (echo's built-in CORS, chi's `middleware.Logger`)
  does not transfer. Write project middleware in the stdlib shape so it survives
  a swap; use framework-provided middleware only inside the adapter.

## Go conventions

- Wire through constructors (`user.NewService(repo user.Repository) *user.Service`).
  No global state, no `init()` that touches I/O.
- Interfaces are declared by the **consumer** and kept small. Do not create one
  until a second implementation or a test genuinely needs it.
- `context.Context` is the first parameter of every function that does I/O, and is
  propagated — never `context.Background()` below `main`.
- Errors: wrap with `fmt.Errorf("...: %w", err)`. Sentinel errors
  (`var ErrUserNotFound = errors.New(...)`, `ErrEmailTaken`) live in
  `internal/user` and are matched with `errors.Is`. **Each driving adapter owns
  its own error mapping** — `handler/` maps to HTTP status codes, `gapi/` maps to
  gRPC codes. The service knows about neither.
- `gofmt` on everything; `go vet ./...` clean.

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
is added later. Log an error once, at the boundary that handles it — do not log
and also return.

## Brief-specific requirements

Explicitly graded and easy to miss:

- **Email uniqueness** must be enforced by a **unique index in MongoDB**, not just
  an application-level check — the check alone races. The `mongodb` adapter owns
  both the index creation and the mapping of the duplicate-key error to
  `ErrEmailTaken`. (This one detail signals the JD's concurrency awareness better
  than any amount of CRUD.)
- **Passwords** are hashed with `bcrypt`. The hash never appears in any API
  response, gRPC message, or log line.
- **JWT** signed `HS256` with a secret from config. Verification must check the
  signing method matches before trusting claims. The secret has no default —
  startup fails if missing.
- **JWT for gRPC** comes from request metadata, not an `Authorization` header —
  same `auth` package, different extraction. Extraction lives in each adapter,
  verification in `internal/auth`.
- **Logging middleware** captures method, path, and execution time. Written as
  `func(http.Handler) http.Handler` so it survives a framework swap.
- **Background goroutine** logs the total user count every 10 seconds. Takes a
  `context.Context`, exits on cancel, uses a `time.Ticker` not `time.Sleep`.
  Started in `main`, stopped on shutdown.
- **Graceful shutdown** on SIGINT/SIGTERM: stop accepting HTTP connections,
  `GracefulStop` the gRPC server, cancel the background goroutine, disconnect Mongo.
- **Input validation** on every write endpoint: required fields, email format.
  Validation errors return 400 (HTTP) / `InvalidArgument` (gRPC), never 500.
- **README.md** is a graded deliverable: setup and run instructions, how to
  generate and use a JWT, sample requests and responses, documented assumptions.
  Real documentation, not a stub — the JD lists code review and technical design
  contribution as a Responsibility, and this is where writing gets assessed.

### gRPC scope

The brief asks only for `CreateUser` and `GetUser` in the proto. Do not mirror the
full REST CRUD surface into gRPC — it is not credited and it doubles the
maintenance. `user.pb.go` is generated by `protoc` via a Makefile target and
**committed** to the repo, per the house convention.

## Commands

Makefile follows go-blueprint's target names, plus `protoc` generation per the
house convention:

```bash
make run          # go run ./cmd/api
make build
make watch        # live reload via air
make lint         # gofmt check + go vet, same as CI
make test         # unit tests only: go test -short -race -v ./...
make itest        # everything including integration: needs MongoDB running
make proto        # protoc generation into api/user/v1/
make docker-run   # docker compose up --build  (api + mongo)
make docker-down
make clean
```

`make test` passes `-short`, so integration tests must guard on
`testing.Short()` to stay out of it. `make itest` drops the flag and runs the
whole suite. CI runs both as separate jobs, plus a Docker image build.

Prefer `make` over raw `go` commands so behaviour matches CI. The GitHub Actions
workflow runs the same targets — if it passes locally via `make`, it passes in CI.

## Testing

The brief grades "test coverage and effective mocking" — not optional.

- Go's standard `testing` package. No mocking framework; hand-write fakes.
- **`service_test.go`** sits next to `service.go` per the house convention and
  uses a fake `Repository` defined in the test file. This is the payoff of the
  port interface: the entire business logic suite runs with no database and no
  HTTP.
- **Handler tests** use `httptest` against `Router.Handler()`, asserting status
  code and response body, including auth-rejection paths (missing token, bad
  signature, expired).
- **gapi tests** call the server methods directly with a fake service —
  no network needed.
- **Auth tests** cover token round-trip and tampering rejection.
- **Integration tests** live in `test/integration/`, run against a real MongoDB in
  Docker via `make itest`, guarded by `testing.Short()` so `make test` stays fast.
  The unique-index behaviour must be covered here — it cannot be tested with a fake.
- Table-driven where there is more than one case. Test behaviour, not
  implementation.

## Configuration

- Env-based config in `internal/config`, loaded once in `main` into a typed
  struct. Nothing else calls `os.Getenv`.
- `.env` is local-only and gitignored. Commit `.env.example` listing every
  variable with placeholder values.
- Fail fast at startup on a missing required variable. No defaults for the JWT
  secret or the Mongo URI.
- The house convention lists Vault as a future direction — out of scope here.

## Lottery Search System (deliverable 2)

Design document only — **do not write implementation code for this.** It goes in
`docs/lottery-search-design.md`.

**Treat this as the more important deliverable of the two.** The user API
demonstrates Golang, gRPC, REST, MongoDB, and Docker. This document is the only
place the remaining JD Qualifications can be shown: Redis for caching and
**distributed locking**, message queues, distributed systems, performance
optimisation, and high concurrency. A thin design doc leaves most of the
Qualifications list unevidenced.

Required coverage, from the brief:

- Architecture, data structures, and the wildcard matching algorithm
- Indexing strategy for patterns like `****23`, `1****5`, `123***` at 10M scale
- Production storage choice, with justification
- How duplicate simultaneous allocation of the same ticket is prevented
- Performance analysis and tradeoffs

Angles worth working through, given what the JD is testing for:

- **The search space is small and bounded.** Six digits is only 10^6 distinct
  numbers, while the dataset is 10M tickets — so numbers repeat, and the problem
  is really "find the tickets holding a matching number", not "scan 10M rows".
  There are exactly 2^6 = 64 wildcard mask shapes. Both facts open up
  precomputation that a naive regex scan misses. Say so explicitly; noticing this
  is the difference between a feasible design and a slow one.
- **Indexing options to weigh:** per-position inverted index (position, digit) →
  ticket set, intersected across the fixed positions; versus a materialised index
  keyed by masked pattern; versus MongoDB with digits split into six indexed
  fields. Compare, do not just pick.
- **Allocation without duplicates** is the crux and the part most candidates get
  wrong. Discuss atomic pop versus reserve-with-TTL: Redis `SPOP` removes and
  returns a member atomically, so two concurrent users cannot receive the same
  ticket without any lock at all. Contrast with a lock-based approach and with
  reservation keys (`SET NX` + expiry) that return abandoned tickets to the pool.
  Cover what happens when a user abandons a reservation, and the failure mode if
  Redis restarts.
- **Distributed locking** where it is genuinely needed versus where an atomic
  primitive removes the need for a lock — this distinction is exactly what a
  senior backend interview probes for.
- **Concurrency and scale:** what happens at 10k concurrent searches, where the
  hot keys are, and how the design shards or replicates.
- **Kafka** only if it earns its place (for example async allocation
  confirmation or an audit trail). Do not include it to tick the box; an
  unjustified queue reads worse than no queue.

## Out of scope

The house convention's roadmap includes OpenTelemetry tracing/metrics, Kafka,
DB transactions, and Vault-based config. **None belong in the API code for this
test.** The slog style above is written so an OTel bridge can be dropped in later
without touching call sites. Redis and Kafka belong in the design document, not
in `internal/`.

## Working notes for Claude

- Do not create empty directories or placeholder files. Add structure when there
  is code for it.
- When adding a second domain, copy the `user` package's shape exactly rather
  than inventing a variation.
- Prefer the standard library. Before adding a dependency, say what it does that
  stdlib cannot.
- Run `make test` after changes. Report failures with actual output, not a summary.
- Keep to the brief. Extra features in the API are not credited; the bonus list in
  [README-2.md](README-2.md) is the only sanctioned expansion of the code.

## Dependencies

Deliberately few. Everything else is standard library — config parsing, JSON,
HTTP, and logging need no third party here.

- `github.com/go-chi/chi/v5` — router, reachable only from `internal/router/chirouter`
- `go.mongodb.org/mongo-driver/v2` — **v2, not v1.** The API differs: `mongo.Connect`
  takes options only and no `context.Context`, and it does not contact the server,
  so a `Ping` is what turns an unreachable database into a startup failure.

Still to be added with the domain: a JWT library and `golang.org/x/crypto/bcrypt`
(already present as an indirect dependency of the Mongo driver).

## Local tooling

Available: Go 1.26.6, Docker, `air`, `migrate`.
Not installed — install on demand: `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc`
(needed for gRPC), `golangci-lint`, `go-blueprint`.

## Next steps

1. Design artefacts in `docs/`: user stories, use case diagram, UML sequence
   diagrams. **Before** any domain code.
2. `api/user/v1/user.proto` with `CreateUser` and `GetUser`, then `make proto`.
3. The `user` domain, following the diagrams: entity, service, port, then the
   `mongodb`, `handler`, and `gapi` adapters.
4. `internal/auth`: JWT issue/verify and bcrypt hashing.
5. `README.md` — a graded deliverable, not a stub.
6. `docs/lottery-search-design.md`.
