# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project

An e-commerce backend in Go, built as a set of domain services behind REST and
gRPC.

- Module path: `github.com/DoIttikorn/e-commerce`
- Go: 1.26.x (1.26.6 installed locally)
- Storage: MongoDB

The **User Management API** is the first domain and the current focus. Its
requirements are in [README-2.md](README-2.md), which also contains a second,
design-only deliverable (the Lottery Search System). Both are due before the
wider e-commerce work starts — see [Next steps](#next-steps).

Local-only context, not committed (see `.gitignore`):

- `NOTES.local.md` — how the current work is being assessed, and what is and is
  not credited. Read it before deciding what to build.
- `Test-go.md` — the role this work is aimed at.
- `Project structure Golang.md` — the owner's own Go layout standard, which is
  reference #1 below.

## Current state

**The skeleton is built and green; no domain is written yet.**

In place and passing `make lint` and `make test`:

- `internal/config` — env loading, fail-fast, reports every problem at once
- `internal/router` + `internal/router/chirouter` — the routing port and its chi
  adapter, with tests proving `r.PathValue` works through it
- `internal/database` — Mongo connect and reachability ping
- `internal/middleware` — request logging
- `cmd/api/main.go` — wiring, `/healthz`, graceful shutdown
- `Makefile`, `Dockerfile`, `docker-compose.yml`, `.github/workflows/ci.yml`,
  `.air.toml`, `.env.example`, `.gitignore`, `.dockerignore`

Not written yet: every domain package, `internal/auth`, and `api/`.

### Design before domain

Domain code waits on design artefacts, which go in `docs/`:

- user stories
- use case diagram
- UML sequence diagrams

Write a domain only after those exist for it, and make the code follow them. If a
sequence diagram and the code disagree, one of them is wrong — say so rather than
quietly implementing something else.

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
├── api/
│   └── user/v1/                     # one directory per domain contract
│       ├── user.proto
│       └── user.pb.go               # generated, committed
├── cmd/
│   └── api/main.go                  # wiring, starts HTTP + gRPC, graceful shutdown
├── internal/
│   ├── config/                      # env-based config
│   ├── database/                    # shared driver init
│   │   └── mongo.go                 # client construction + reachability ping
│   ├── middleware/                  # stdlib-shaped HTTP middleware
│   │   └── logging.go
│   ├── router/                      # HTTP framework abstraction
│   │   ├── router.go                # PORT: the Router interface
│   │   └── chirouter/               # chi adapter
│   ├── auth/                        # JWT issue/verify, password hashing, middleware
│   └── user/                        # first domain — the template for the rest
│       ├── user.go                  # entity + domain errors
│       ├── service.go               # business logic
│       ├── service_test.go          # unit tests with fakes
│       ├── repository.go            # PORT: Repository interface only
│       ├── mongodb/                 # DRIVEN adapter
│       ├── handler/                 # DRIVING adapter: REST
│       └── gapi/                    # DRIVING adapter: gRPC
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

This matters more with every domain added:

- A domain never imports another domain's `mongodb`, `handler`, or `gapi` package.
- Cross-domain calls go through a **service interface declared by the caller**.
  If `order` needs users, `order` declares the narrow interface it needs
  (`type UserLookup interface { ByID(ctx, id) (User, error) }`) and `main` wires
  `user.Service` into it.
- Do not share entities between domains. If `order` needs a customer name, it
  defines its own small type. Shared entities couple domains together and are the
  usual reason a "microservice" layout cannot actually be split.
- No domain reads another domain's collections.

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
  CRUD into gRPC. `user.pb.go` is generated by `make proto` and **committed**.

## Commands

```bash
make run          # go run ./cmd/api
make build
make watch        # live reload via air
make lint         # gofmt check + go vet, same as CI
make test         # unit tests only: go test -short -race -v ./...
make itest        # everything including integration: needs MongoDB running
make proto        # protoc generation into api/<domain>/v1/
make docker-run   # docker compose up --build  (api + mongo)
make docker-down
make clean
```

`make test` passes `-short`, so integration tests must guard on
`testing.Short()` to stay out of it. `make itest` drops the flag. CI runs both as
separate jobs, plus a Docker image build.

## Testing

- Go's standard `testing` package. No mocking framework; hand-write fakes.
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
- `.env` is local-only and gitignored; `.env.example` is the committed template
  and must list every variable.
- Fail fast at startup on a missing or invalid required variable, and report every
  problem at once. No defaults for the JWT secret or the Mongo URI.

## Lottery Search System

A design document only — **do not write implementation code for it.** It goes in
`docs/lottery-search-design.md`. Requirements are in [README-2.md](README-2.md);
`NOTES.local.md` explains why this document carries more weight than its
"no code required" framing suggests, and lists the angles worth covering.

## E-commerce roadmap

Domains to come, once the User domain and the design document are done:
Seller, Product, Order, Cart, Payment, Marketplace, Live Commerce.

Each arrives as a full domain package per [Adding a domain](#adding-a-domain).
Infrastructure the house convention anticipates — Redis for caching and
distributed locking, Kafka for asynchronous workflows, OpenTelemetry — is added
**when a domain actually needs it**, never up front. A dependency with no caller
is worse than no dependency.

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
  a `Ping` is what turns an unreachable database into a startup failure.

To be added with the domain: a JWT library, `golang.org/x/crypto/bcrypt` (already
an indirect dependency), and `google.golang.org/grpc`.

## Local tooling

Available: Go 1.26.6, Docker, `air`, `migrate`.
Not installed — install on demand: `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc`,
`golangci-lint`, `go-blueprint`.

## Next steps

1. Design artefacts in `docs/` for the User domain: user stories, use case
   diagram, UML sequence diagrams. **Before** any domain code.
2. `api/user/v1/user.proto` with `CreateUser` and `GetUser`, then `make proto`.
3. `internal/user`: entity, service, port, then the `mongodb`, `handler`, and
   `gapi` adapters.
4. `internal/auth`: JWT issue/verify and bcrypt hashing.
5. `README.md` — setup, JWT guide, sample requests, documented decisions.
6. `docs/lottery-search-design.md`.
7. Only then: the next e-commerce domain.
