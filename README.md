# e-commerce

A Go backend built as six independently deployable services — **user**,
**seller**, **product**, **order**, **marketplace** and **live** — sharing one
repository. They serve REST, gRPC and WebSocket; each owns its own MongoDB
instance; and they reach each other through Kafka events rather than by calling
each other, with exactly one deliberate exception.

- **Start here — the one trace that exercises everything, and the decisions that
  were not obvious:** [docs/highlights.md](docs/highlights.md)
- **Architecture and reasoning:** [docs/domains.md](docs/domains.md)
- **Why no service publishes from a request path:**
  [docs/transactional-outbox.md](docs/transactional-outbox.md)
  · [ภาษาไทย](docs/transactional-outbox.th.md)
- **Technology choices, and what each costs:** [docs/tech-stack.md](docs/tech-stack.md)
- **User domain design — stories, use cases, sequence diagrams:** [docs/user-domain-design.md](docs/user-domain-design.md)
- **Measured performance:** [test/load/README.md](test/load/README.md)

## Quick start

Requires Docker and Go 1.26.

```bash
cp .env.example .env
# Set JWT_SECRET to at least 32 characters. The service refuses to start
# without one rather than falling back to a default nobody changes.
echo "JWT_SECRET=$(openssl rand -base64 48)" >> .env

make docker-run
```

That builds three images and starts six containers. When it settles:

```bash
curl -s localhost:8080/healthz   # {"status":"ok"}
curl -s localhost:8081/readyz    # {"status":"ready"}
curl -s localhost:8082/readyz    # {"status":"ready"}
```

`make` on its own lists every target.

## Services

| Service | REST | gRPC | Admin | MongoDB | Notes |
|---|---|---|---|---|---|
| **user** | 8080 | 9090 | 6060 | 27017 | issues every token |
| **seller** | 8081 | — | 6061 | 27018 | publishes shop events |
| **product** | 8082 | internal | 6062 | 27019 | catalogue, Redis cache, stock reservation |
| **order** | 8083 | — | 6063 | 27020 | the saga; transactional outbox |
| **marketplace** | 8084 | — | 6064 | 27021 | search projection from three streams |
| **live** | 8085 | — | 6065 | 27022 | WebSocket; Redis presence and broadcast |

Plus two development tools, both described below: **Kafka UI on
[localhost:8090](http://localhost:8090)** for watching the event flow, and
**Jaeger on [localhost:16686](http://localhost:16686)** for following one
request across every service it touches.

Each owns a **separate MongoDB instance**, not a separate database on a shared
server. Connection limits, cache, lock contention, backups, version upgrades and
failures are all things a shared instance keeps shared regardless of how the
databases are named.

Every instance is a single-node replica set, because transactions and change
streams both need an oplog. From the host, connect with `directConnection=true`:
each set advertises its member as `mongo-<service>:27017`, a name that resolves
inside the compose network and nowhere else.

One member is a development convenience and buys no redundancy. The code is
written for more: the driver is configured for `majority` writes and reads, so
an acknowledged write survives a failover and a read never returns something an
election could erase. On a one-member set `majority` is identical to `w:1` and
costs nothing — which is why it is set now, rather than being a change somebody
has to remember when members are added.

That claim is checkable rather than asserted:

```bash
make docker-run-ha     # three-member set for Product; everything else unchanged

docker compose exec mongo-product mongosh --quiet --eval \
  'rs.status().members.forEach(m => print(m.name + "  " + m.stateStr))'
# mongo-product:27017    PRIMARY
# mongo-product-2:27017  SECONDARY
# mongo-product-3:27017  SECONDARY

docker compose stop mongo-product          # kill the primary
curl -s localhost:8082/readyz              # {"status":"ready"}
curl -s 'localhost:8082/api/v1/products?limit=1' | jq '.total'
```

A secondary is elected, the driver finds it, and reads and writes carry on —
with no error logged and readiness never dropping. Not one line of Go changes
for it, which is the whole point: the URI already names the set, and the driver
already discovers members. Three members for all six services would be eighteen
mongod processes, so it is an opt-in overlay
([docker-compose.ha.yml](docker-compose.ha.yml)) rather than the default.

The admin port carries Prometheus metrics and pprof. It is never the API port —
pprof can dump process memory and stall the process, so it belongs on an
internal interface in a real deployment.

## Try it

Copy-paste, in order. Every command below was run against the running stack and
the outputs are real. `jq` is used for readability — `brew install jq` if you
do not have it, or drop the pipes and read the raw JSON.

### 1 — Create an account and get a token

```bash
export EMAIL="you-$(date +%s)@example.com"
export PASSWORD="correct-horse-battery"

curl -s -X POST localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"Ittikorn\",\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}"
```

```json
{
  "id": "6a86d38010080d03d24116bc",
  "name": "Ittikorn",
  "email": "you-1787220864@example.com",
  "created_at": "2026-08-20T10:14:24.851726294Z"
}
```

No password field, in this or any other response. The wire types have nowhere
to put one, so a hash cannot leak by accident.

```bash
export TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" | jq -r .token)

echo "${TOKEN:0:40}..."
```

### 2 — Open a shop on the **seller** service

The token came from the user service on port 8080; the seller service on 8081
accepts it without either service talking to the other.

```bash
export SHOP="Dodo Ceramics $(date +%s)"

curl -s -X POST localhost:8081/api/v1/sellers \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"shop_name\":\"$SHOP\"}"
```

```json
{
  "id": "6a86d3a05330db8838b9f8c4",
  "user_id": "6a86d38010080d03d24116bc",
  "shop_name": "Dodo Ceramics 1787220896",
  "status": "active",
  "created_at": "2026-08-20T10:14:56.871416254Z"
}
```

```bash
export SELLER_ID=$(curl -s localhost:8081/api/v1/sellers/me \
  -H "Authorization: Bearer $TOKEN" | jq -r .id)
```

The owner is taken from the token, never from the body — you cannot open a shop
in somebody else's name.

### 3 — List a product on the **product** service

```bash
curl -s -X POST localhost:8082/api/v1/products \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Blue Ceramic Mug","description":"320ml, dishwasher safe",
       "price_minor":25000,"currency":"THB","stock":12}'
```

```json
{
  "id": "6a86d3a23999619ac426af61",
  "seller_id": "6a86d3a05330db8838b9f8c4",
  "seller_name": "Dodo Ceramics 1787220896",
  "name": "Blue Ceramic Mug",
  "price_minor": 25000,
  "currency": "THB",
  "stock": 12,
  "created_at": "2026-08-20T10:14:58.019944004Z"
}
```

**Look at `seller_name`.** You did not send it, and the product service did not
ask the seller service for it. It arrived over Kafka a moment after the shop was
created, and the product service keeps its own copy.

Two consequences worth seeing for yourself:

```bash
export PRODUCT_ID=$(curl -s "localhost:8082/api/v1/products?limit=1" | jq -r '.products[0].id')

# The catalogue is public. No token.
curl -s "localhost:8082/api/v1/products/$PRODUCT_ID" | jq '{name, seller_name}'
```

If you are quick enough to create a product before the event lands, you get a
**409**, not a wrong answer:

```json
{"error":"no shop is registered for this account yet; if you have just created one, retry shortly"}
```

That is eventual consistency being honest rather than inventing a blank name.

### 4 — Rename the shop and watch the product follow

This is the whole point of the event stream.

```bash
curl -s -X PATCH "localhost:8081/api/v1/sellers/$SELLER_ID" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"shop_name":"Dodo Pottery"}' | jq -r .shop_name

# Poll the product service. It has not been told anything directly.
for i in $(seq 1 20); do
  sleep 0.4
  curl -s "localhost:8082/api/v1/products/$PRODUCT_ID" | jq -r .seller_name
done | uniq
```

```
Dodo Ceramics 1787220896
Dodo Pottery
```

Measured at **~400ms** locally. The seller service published, the product
service consumed, updated every product that shop owns, and dropped exactly
those entries from Redis. Neither service made a request to the other.

To watch that happen rather than infer it, see [Watching the event flow](#10--watching-the-event-flow).

### 5 — The same user over gRPC

The user service exposes `CreateUser` and `GetUser` on port 9090. Reflection is
registered, so grpcurl needs no copy of the `.proto`:

```bash
brew install grpcurl

grpcurl -plaintext localhost:9090 list
grpcurl -plaintext localhost:9090 describe user.v1.UserService
```

```
user.v1.UserService is a service:
service UserService {
  rpc CreateUser ( .user.v1.CreateUserRequest ) returns ( .user.v1.CreateUserResponse );
  rpc GetUser ( .user.v1.GetUserRequest ) returns ( .user.v1.GetUserResponse );
}
```

**The token travels in metadata, not in an HTTP header** — the one part of
authentication that genuinely differs between the two adapters:

```bash
export USER_ID=$(curl -s localhost:8080/api/v1/users -H "Authorization: Bearer $TOKEN" | jq -r '.users[0].id')

grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d "{\"id\":\"$USER_ID\"}" localhost:9090 user.v1.UserService/GetUser
```

```json
{
  "user": {
    "id": "6a86d38010080d03d24116bc",
    "name": "Ittikorn",
    "email": "you-1787220864@example.com",
    "createdAt": "2026-08-20T10:14:24.851Z"
  }
}
```

```bash
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"name":"Somchai","email":"somchai@example.com","password":"another-long-enough-password"}' \
  localhost:9090 user.v1.UserService/CreateUser
```

The gRPC surface is deliberately **not** a mirror of the REST one. There is no
`Login` and there never will be: a service must never authenticate *as* a user,
it propagates the user's token. See [docs/tech-stack.md](docs/tech-stack.md).

### 6 — The failures are worth trying too

The same domain error becomes an HTTP status or a gRPC code depending on which
adapter you reach it through. The service that raised it knows about neither.

```bash
# No token
grpcurl -plaintext -d "{\"id\":\"$USER_ID\"}" localhost:9090 user.v1.UserService/GetUser
#   Code: Unauthenticated

# Malformed ID, then a well-formed one that does not exist
grpcurl -plaintext -H "authorization: Bearer $TOKEN" -d '{"id":"not-an-object-id"}' \
  localhost:9090 user.v1.UserService/GetUser
#   Code: InvalidArgument
grpcurl -plaintext -H "authorization: Bearer $TOKEN" -d '{"id":"000000000000000000000000"}' \
  localhost:9090 user.v1.UserService/GetUser
#   Code: NotFound
```

```bash
# Duplicate email -> 409, raised by the MongoDB unique index, not a
# read-then-write check, which would race under concurrent registration.
curl -s -X POST localhost:8080/api/v1/auth/register -H 'Content-Type: application/json' \
  -d "{\"name\":\"Dup\",\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}"
# {"error":"email already registered"}

# Validation -> 400, naming every offending field at once
curl -s -X POST localhost:8080/api/v1/auth/register -H 'Content-Type: application/json' \
  -d '{"name":"","email":"nope","password":"x"}'
```

```json
{
  "error": "validation failed",
  "fields": {
    "email": "must be a valid email address",
    "name": "is required",
    "password": "must be at least 8 characters"
  }
}
```

```bash
# Somebody else's product -> 403
# {"error":"this product belongs to another shop"}

# A wrong password and an unknown email give an identical 401, so login cannot
# be used to discover which addresses are registered.
curl -s -X POST localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"wrong\"}"
curl -s -X POST localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"nobody@example.com","password":"whatever"}'
# both: {"error":"invalid credentials"}
```

### 7 — Place an order

This is the one place a service calls another and waits. Order asks Product to
reserve stock over gRPC, because a buyer cannot be told their order exists until
the stock is secured.

**`Idempotency-Key` is required.** It is not generated server-side: a key the
server invents is a new key on every retry, which is the same as having none.
Placing an order is the one operation here that spends money, so the caller has
to decide what "the same order" means.

```bash
export PRODUCT_ID=$(curl -s "localhost:8082/api/v1/products?limit=1" | jq -r '.products[0].id')

curl -s -X POST localhost:8083/api/v1/orders \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: order-$(date +%s)" \
  -d "{\"items\":[{\"product_id\":\"$PRODUCT_ID\",\"quantity\":3}]}" | jq
```

```json
{
  "id": "6a8717246cfb8e2fcbbdf743",
  "seller_id": "6a8716f4...",
  "status": "pending",
  "lines": [
    {
      "product_id": "6a87171401a76356ad521cc2",
      "product_name": "Blue Ceramic Mug",
      "unit_minor": 25000,
      "quantity": 3,
      "subtotal_minor": 75000
    }
  ],
  "total_minor": 75000,
  "currency": "THB"
}
```

The price and name are **snapshots**, not references: a seller who reprices
tomorrow must not change what somebody agreed to pay today.

Stock came out of the Product service's own database, over gRPC:

```bash
curl -s "localhost:8082/api/v1/products/$PRODUCT_ID" | jq '{stock}'
# {"stock": 7}   — it was 10
```

Retry with the same key and nothing is bought twice — the original order comes
back with **200** instead of 201:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8083/api/v1/orders \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: the-same-key-as-before" \
  -d "{\"items\":[{\"product_id\":\"$PRODUCT_ID\",\"quantity\":3}]}"
```

Cancelling puts the stock back. Paying keeps it:

```bash
export ORDER_ID=$(curl -s localhost:8083/api/v1/orders -H "Authorization: Bearer $TOKEN" | jq -r '.orders[0].id')

curl -s -X POST "localhost:8083/api/v1/orders/$ORDER_ID/pay" \
  -H "Authorization: Bearer $TOKEN" | jq '{id, status}'
# {"id": "...", "status": "paid"}
```

Ordering more than exists returns **409**, not 400 — the request was correct and
may well succeed later:

```json
{"error":"one or more items are out of stock"}
```

### 8 — Search the marketplace

The Marketplace service has no write API. Everything in it arrived as an event
from Product, Seller or Order, and it answers a question none of them can
answer alone.

```bash
curl -s "localhost:8084/api/v1/marketplace/listings?q=ceramic&sort=best_selling" \
  | jq '.listings[0]'
```

```json
{
  "product_id": "6a87171401a76356ad521cc2",
  "seller_name": "Six Domains Pottery",
  "name": "Blue Ceramic Mug",
  "price_minor": 25000,
  "in_stock": true,
  "sold_count": 3
}
```

Three separate streams produced that one row: the product and its price from
`product.events`, the shop name from `seller.events`, and `sold_count` from the
order you just paid.

```bash
# The text index actually discriminates — it is not a substring match.
curl -s "localhost:8084/api/v1/marketplace/listings?q=ceramic" | jq .total   # 1
curl -s "localhost:8084/api/v1/marketplace/listings?q=bicycle" | jq .total   # 0

# Filter and sort.
curl -s "localhost:8084/api/v1/marketplace/listings?min_price=10000&max_price=30000&sort=price_asc&in_stock=true" | jq '.total'
```

### 9 — Watch a live stream

The host drives the stream over REST; viewers watch over a WebSocket. Watching
is public — a browser cannot set headers on a WebSocket handshake, so an
authenticated socket ends up with the token in the query string, which is the
one place credentials are guaranteed to reach an access log.

```bash
export STREAM_ID=$(curl -s -X POST localhost:8085/api/v1/live/streams \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"Friday Pottery Sale"}' | jq -r .id)

curl -s -X POST "localhost:8085/api/v1/live/streams/$STREAM_ID/start" \
  -H "Authorization: Bearer $TOKEN" | jq '{status}'

curl -s -X POST "localhost:8085/api/v1/live/streams/$STREAM_ID/feature" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"product_id\":\"$PRODUCT_ID\"}" | jq '{featured_product_id}'
```

Connect a viewer with any WebSocket client — `websocat` is convenient:

```bash
brew install websocat
websocat "ws://localhost:8085/api/v1/live/streams/$STREAM_ID/watch"
```

The first message is a snapshot of what you are looking at:

```json
{"type":"stream.state","stream_id":"...","viewers":1,"featured_product_id":"...","status":"live","at":"..."}
{"type":"viewer.joined","stream_id":"...","viewers":1,"at":"..."}
```

Now place and pay an order for the featured product in another terminal. The
purchase arrives on the socket:

```json
{"type":"purchase","stream_id":"...","viewers":1,"featured_product_id":"...","product_name":"Blue Ceramic Mug","quantity":2,"at":"..."}
```

**Trace what just happened.** The order was paid on port 8083. That service
wrote an event to its outbox in the same transaction, a relay published it to
Kafka, the live service consumed it, asked its own database which streams are
showing that product, and published to a Redis channel — which reached the
instance holding your socket. The Order service has never heard of live streams,
and nothing about it had to change for this to work.

### 10 — Watching the event flow

An event-driven system is hard to reason about precisely because the interesting
part happens between the services rather than inside either one. `make docker-run`
starts a Kafka browser for exactly that:

```bash
make kafka-ui        # or open http://localhost:8090
```

**Topics → `seller.events` → Messages** shows every event the seller service has
published, newest first, with its key and payload:

```json
{
  "type": "seller.updated",
  "seller_id": "6a86ca74786fc05be09fca6e",
  "user_id": "6a86ca746884bbe1083d9703",
  "shop_name": "Renamed Shop",
  "status": "active",
  "occurred_at": "2026-08-20T09:36:40.531951927Z"
}
```

Three things there are worth noticing:

- **The key is the seller ID**, visible in the Key column. Ordering in Kafka is
  per partition, so keying by the entity is what stops two renames of one shop
  from being applied backwards.
- **`user_id` travels with the event.** That is what lets the product service
  answer "which shop does this caller own?" from its own database instead of
  calling the seller service on every write.
- **One topic carries both event types**, discriminated by `type`. A consumer
  that only cares about renames reads the whole topic and ignores the rest,
  which is what lets the publisher add an event type without coordinating.

**Consumers → `product-service`** shows the consumer group: its state, its
members, and its **lag**. Lag is the number the health of an event-driven system
lives or dies by — a lag that climbs means the consumer is falling behind the
producer, and no amount of green health checks will tell you that.

Rename a shop with the stack open on that page and watch the offset advance.

> The UI is a development tool with no authentication configured. It belongs on
> a laptop or an internal network, never on a public one.

### 11 — Follow one order across all six services

A request ID ties together the log lines of one request *inside* one process.
The moment the work crosses a gRPC call or an event, it stops. Placing an order
touches Order, Product, the outbox, Kafka, and Marketplace — five hops and four
processes — and a request ID gets you through none of them.

`make docker-run` starts Jaeger for that:

```bash
make traces          # or open http://localhost:16686
```

Place an order (step 7 above), then pick **Service: order**, **Operation:
`POST /api/v1/orders/`**, and **Find Traces**. One trace, and reading down it —
this is a real one, copied out of the API:

```
order         POST /api/v1/orders/                 138ms   (server)
order         product.v1.StockService/Reserve       65ms   (client)
product       product.v1.StockService/Reserve       28ms   (server)
order         product.v1.StockService/Confirm        1ms   (client)
product       product.v1.StockService/Confirm        0ms   (server)
order         outbox publish order.events           20ms   (producer)
marketplace   consume order.events                   0ms   (consumer)
```

The last two lines are the ones worth the effort. The publish did not happen
during the request — the request wrote the event into the outbox inside its
transaction and returned, and a background relay published it afterwards. The
trace context is stored in the outbox row and replayed by the relay, so the
event and the checkout that caused it are one trace instead of two unrelated
log streams and a timestamp comparison.

Two more things to try:

```bash
# Every log line carries trace_id and span_id, so a line found in the logs and
# the whole distributed request are one paste apart.
make docker-logs SERVICE=order | grep trace_id | head -1

# Send your own trace ID in and watch it come back out in the logs.
curl -s -o /dev/null localhost:8083/healthz \
  -H 'traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'
make docker-logs SERVICE=order | grep 4bf92f3577b34da6a3ce929d0e0e4736
```

The second one is the whole contract in one command: a trace started somewhere
else — an API gateway, a mobile client, another team's service — is continued
here rather than replaced.

Span names are route patterns, never paths: `GET /api/v1/users/{id}`, not
`GET /api/v1/users/68f1…`. That is the same rule the Prometheus labels follow
and for the same reason — one span name per user ID is unbounded cardinality in
the trace backend, where it costs indexing and money rather than a crash. It is
also why the HTTP instrumentation here is hand-written instead of `otelhttp`:
the standard middleware names a span before the router has matched, so the only
name available to it is the raw path.

> Jaeger `all-in-one` keeps traces in memory and loses them on restart, which is
> right for a laptop and wrong everywhere else.

### 12 — Look at what it is doing

```bash
# Cache hit rate on the product service
curl -s localhost:6062/metrics | grep product_cache_lookups_total

# Request rate, latency and status, labelled by route pattern — never by raw
# path, which would mint a time series per product ID.
curl -s localhost:6062/metrics | grep http_requests_total

# Profiling, on the admin port only
go tool pprof http://localhost:6062/debug/pprof/heap

# The API port does not serve it: 404
curl -s -o /dev/null -w '%{http_code}\n' localhost:8082/debug/pprof/heap
```

Every log line for a request carries the same `request_id`, and the header is
echoed back so you can follow one request through:

```bash
curl -s -D- -o /dev/null localhost:8080/healthz -H 'X-Request-ID: trace-me-123' | grep -i x-request-id
make docker-logs SERVICE=user
```

Full request collections: [test/http/api.http](test/http/api.http) for REST
(VS Code REST Client), [test/grpc/requests.md](test/grpc/requests.md) for gRPC.

## JWT

Tokens are issued **only** by the user service, signed **HS256** with
`JWT_SECRET`, and valid for `JWT_TTL` (default 24h).

```bash
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"correct-horse-battery"}' | jq -r .token)
```

| Where | How to send it |
|---|---|
| REST | `Authorization: Bearer <token>` |
| gRPC | metadata key `authorization`, value `Bearer <token>` |

The payload carries `sub` (the user ID), `iat`, and `exp` — nothing else, and
never anything secret. It is signed, not encrypted; anyone holding it can read it.

```bash
# JWT uses base64url without padding, which `base64 -d` will not accept.
decode() { python3 -c 'import base64,sys,json;s=sys.stdin.read().strip();print(json.dumps(json.loads(base64.urlsafe_b64decode(s+"="*(-len(s)%4))),indent=2))'; }

echo "$TOKEN" | cut -d. -f1 | decode   # header
echo "$TOKEN" | cut -d. -f2 | decode   # payload
```

```json
{ "alg": "HS256", "typ": "JWT" }
{
  "sub": "6a86d38010080d03d24116bc",
  "exp": 1787307265,
  "iat": 1787220865
}
```

Verification pins the algorithm before anything else. Accepting the algorithm a
token names is the classic JWT bypass — a token declaring `alg: none` carries no
signature, and one declaring `RS256` can be signed with the public key as an
HMAC secret. There is a test for both.

Both schemes are built, and which one runs is a configuration choice:

- **`JWT_SECRET` (HS256)** — what the brief specifies, and what a single service
  needs. Everyone holding the secret to verify also holds the secret to sign,
  which is unremarkable when there is only one everyone.
- **A key pair (EdDSA)** — `make keys` prints both halves. Give
  `JWT_PRIVATE_KEY` to the user service and nothing else; every other service
  gets only `JWT_PUBLIC_KEY` and therefore cannot mint a token however
  thoroughly it is compromised. Set them and they take precedence over
  `JWT_SECRET`.

A public key with no private key is the correct, expected configuration for the
five verify-only services — not half a pair.

## API

### user — `:8080`

| Method | Path | Auth |
|---|---|---|
| POST | `/api/v1/auth/register` | public |
| POST | `/api/v1/auth/login` | public |
| GET | `/api/v1/users` | bearer |
| POST | `/api/v1/users` | bearer |
| GET | `/api/v1/users/{id}` | bearer |
| PATCH | `/api/v1/users/{id}` | bearer, self only |
| DELETE | `/api/v1/users/{id}` | bearer, self only |

gRPC on `:9090` — `user.v1.UserService/CreateUser`, `.../GetUser`.

### seller — `:8081`

| Method | Path | Auth |
|---|---|---|
| POST | `/api/v1/sellers` | bearer |
| GET | `/api/v1/sellers` | bearer |
| GET | `/api/v1/sellers/me` | bearer |
| GET | `/api/v1/sellers/{id}` | bearer |
| PATCH | `/api/v1/sellers/{id}` | bearer, owner only |

### product — `:8082`

| Method | Path | Auth |
|---|---|---|
| GET | `/api/v1/products` | **public** |
| GET | `/api/v1/products/{id}` | **public** |
| POST | `/api/v1/products` | bearer |
| PATCH | `/api/v1/products/{id}` | bearer, owner only |
| DELETE | `/api/v1/products/{id}` | bearer, owner only |

### order — `:8083`

| Method | Path | Auth |
|---|---|---|
| POST | `/api/v1/orders` | bearer + `Idempotency-Key` |
| GET | `/api/v1/orders` | bearer, own only |
| GET | `/api/v1/orders/{id}` | bearer, buyer only |
| POST | `/api/v1/orders/{id}/cancel` | bearer, buyer only |
| POST | `/api/v1/orders/{id}/pay` | bearer, buyer only |

### marketplace — `:8084`

| Method | Path | Auth |
|---|---|---|
| GET | `/api/v1/marketplace/listings` | **public** |

Query: `q`, `seller_id`, `min_price`, `max_price`, `in_stock`, `sort`
(`relevance`, `newest`, `price_asc`, `price_desc`, `best_selling`), `limit`, `offset`.

### live — `:8085`

| Method | Path | Auth |
|---|---|---|
| GET | `/api/v1/live/streams` | **public** |
| GET | `/api/v1/live/streams/{id}` | **public** |
| GET | `/api/v1/live/streams/{id}/watch` | **public**, WebSocket |
| POST | `/api/v1/live/streams` | bearer |
| POST | `/api/v1/live/streams/{id}/start` | bearer, host only |
| POST | `/api/v1/live/streams/{id}/end` | bearer, host only |
| POST | `/api/v1/live/streams/{id}/feature` | bearer, host only |

### Every service

`GET /healthz` (liveness), `GET /readyz` (readiness), and on the admin port
`GET /metrics` and `/debug/pprof/`.

### Conventions

- JSON field names are `snake_case`.
- Errors are `{"error": "..."}`, with a `fields` object on validation failures.
- **PATCH, not PUT** — an omitted field is left alone, which a struct of plain
  strings could not express.
- Money is an integer count of **minor units** (`price_minor`) plus a currency
  code. Never a float: `0.1 + 0.2` is not `0.3` in binary floating point.
- **400** for a malformed ID, **404** for a well-formed one that does not exist.
- A malformed JSON body is **400**, never 500.

## Design decisions and assumptions

**Three services, not one.** Each owns a database and a binary. They communicate
only by events, which is what makes them deployable and scalable apart.

**Ports and adapters.** A domain's core imports neither `net/http`, nor the
MongoDB driver, nor generated protobuf code. Everything infrastructural arrives
through an interface the domain declares. This is enforced by a test —
`internal/user/architecture_test.go` reads the package's imports and fails on
any of them.

**REST and gRPC are not alternatives.** REST faces clients: browsers cannot speak
gRPC without a proxy, and being able to replay a request with curl at 3am
matters. gRPC faces services, where protobuf and HTTP/2 multiplexing pay off.
Both are driving adapters over one service with no branch inside it.

**Email uniqueness is a database index, not an application check.** A
read-then-write check is passed by both of two concurrent registrations. There
is an integration test that fires eight simultaneous registrations and asserts
exactly one succeeds.

**Login answers identically for a wrong password and an unknown email** — and
still performs a comparison on the unknown-email path, so the response time does
not give away the answer that the message withholds.

**Authorization has no role model, because the brief's entity has no role
field.** Reads are open to any authenticated caller; writes are restricted to
the owner. There is no administrator. `Server.requireSelf` and
`Service.AuthorizeOwner` are the whole rule, and
[docs/user-domain-design.md](docs/user-domain-design.md) records the alternative
that was considered.

**Product holds a copy of the shop name.** A listing page renders hundreds of
products; asking the seller service for each one would be hundreds of calls to
render one page. The copy is kept honest by events. The cost is eventual
consistency, and the API states it rather than hiding it.

**The cache is a decorator over the repository port,** so the service is written
as though it does not exist and turning it off is one line. Redis failures fall
through to MongoDB: a cache that fails the request when it is unavailable has
become a dependency and made availability worse.

**Liveness and readiness are different endpoints.** A failing Kubernetes liveness
probe restarts the pod, so a database check there turns a brief outage into a
restart loop across every instance. `/healthz` checks nothing; `/readyz` checks
dependencies.

**Configuration fails fast and reports every problem at once.** There is no
default for `JWT_SECRET` or `MONGO_URI`. A service that starts with a guessed
security parameter is worse than one that refuses to start.

**Placing an order is a saga, not a transaction.** Stock lives in another
service with another database, so nothing could span both. Reserve, write,
compensate on failure — and the remaining window, a crash between reserving and
writing, is stated below rather than pretended away.

**Reserving stock is an atomic conditional update, not a lock.**
`{_id, stock: {$gte: n}}` with `$inc`: the server matches and decrements in one
step, so twenty buyers racing for the last unit produce exactly one winner.
There is a test that fires twenty concurrent orders at five units and asserts
five orders and zero remaining stock.

**No service publishes from a request path.** Every event is written into the
same transaction as the change that produced it, and a relay publishes it
afterwards. A service that writes and then publishes can succeed at one and fail
at the other, losing the event with no way to detect it. Full reasoning in
[docs/transactional-outbox.md](docs/transactional-outbox.md).

**Marketplace has no write API.** A projection you can edit is no longer a
projection of anything. Popularity is counted once per order ID, because
at-least-once delivery would otherwise inflate every ranking on redelivery.

**Live Commerce keeps nothing in process memory.** A WebSocket lives on one
instance, so viewer counts and broadcasts both go through Redis — presence as a
sorted set scored by time and pruned on read, broadcast as pub/sub. Redis is
required there, unlike in Product and Marketplace where it is only a cache.

**Redis pub/sub for live events, Kafka for everything else.** Pub/sub has no
replay, which is correct for a feed whose value decays in seconds and would be a
disaster for an order.

### Known limitations

Stated because they are real, not because they are hypothetical:

- **Single-node replica sets and a single Kafka broker.** Each service's MongoDB
  is a one-member set: enough for transactions and change streams, no redundancy
  whatsoever. Kafka is one broker at `replicationFactor: 1`. Both are deployment
  decisions rather than code ones — the URIs already name replica sets, the
  driver is configured for `majority` reads and writes, and topics are created
  with `KAFKA_PARTITIONS` (default 3) — but on a laptop they are single points
  of failure and worth saying so.
- **No dead-letter topic.** A message a consumer cannot handle is retried
  forever and blocks its partition behind it. A retry budget plus a DLQ is the
  fix, and it is the next thing to add.
- **No rate limiting or circuit breaking.** The Order → Product call has a
  five-second timeout but no breaker, so a Product service that is slow rather
  than down is absorbed one checkout at a time. Redis is already wired and is
  where a distributed limiter belongs.
- **Nothing watches the outbox depth.** `outbox.PendingCount` is the number to
  page on: a relay that has stopped looks exactly like an idle one until it
  climbs. The metric is available; no alert consumes it.
- **Traces are sampled at 100%,** which is right for a laptop and the first
  number to turn down under real traffic (`OTEL_TRACES_SAMPLER_ARG`).

## Testing

```bash
make test     # unit only, no infrastructure
make itest    # everything, needs the stack up
make lint     # gofmt + vet, exactly what CI runs
```

266 tests. Unit tests use hand-written fakes and no mocking framework —
`service_test.go` runs the whole business-logic suite with no database and no
HTTP, which is what the port interfaces are for.

Integration tests run against real MongoDB, Redis and Kafka. They cover the
things a fake cannot prove: the unique index under concurrent insert, cache
invalidation, and a seller event travelling through a real broker into a real
second service.

Load tests are k6, in [test/load/](test/load/README.md):

```bash
make load-smoke   # 1 user, proves the wiring
make load-read    # the cached catalogue read
make load-auth    # login, which is bcrypt-bound on purpose
```

Measured: **13,818 req/s at p95 7.56ms** on the cached read, **63 req/s at p95
257ms** on login. The 220x gap is bcrypt doing its job. The README there also
records that the cache moves throughput by only 3% while taking MongoDB from
685,062 reads to 4 — the value is protecting the database, not latency.

## Layout

```
api/            published contracts: protobuf and event envelopes
cmd/            one directory per service binary
internal/
  appserver/    the bootstrap every service shares
  auth/         bcrypt, HS256 and EdDSA issue and verify, bearer middleware
  config/       environment configuration, fails fast
  database/     MongoDB and Redis clients
  kafka/        publisher, consumer, topic creation, trace headers
  logging/      JSON logger with request-ID and trace-ID correlation
  middleware/   request ID, tracing, Prometheus metrics, request logging
  outbox/       transactional outbox and its relay
  router/       the routing port and its chi adapter
  servicetls/   mutual TLS for the internal gRPC link
  tracing/      OpenTelemetry setup and propagation
  admin/        metrics and pprof, on their own port
  user/         ─┐
  seller/        ├─ one package per domain, same seven-part shape
  product/      ─┘
test/
  integration/  against real infrastructure
  load/         k6
  http/         REST request collection
  grpc/         grpcurl request collection
docs/           architecture, technology decisions, domain design
```

`chi` is reachable only from `internal/router/chirouter`: handlers are plain
`http.HandlerFunc` and read parameters with `r.PathValue`, so swapping to echo
means writing one adapter, not touching handlers.

## What is not here

`docs/lottery-search-design.md`, and the gaps listed above.
[docs/domains.md](docs/domains.md) has the full map and the reasoning behind
each boundary.
