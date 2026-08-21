# Product — sequence diagrams

*ภาษาไทย: [product.th.md](product.th.md) · Index: [README.md](README.md)*

The catalogue, and the most technically dense of the six. It caches through
Redis, builds a read model from Kafka, and serves stock reservation over gRPC
secured by mutual TLS.

| Flow | Endpoint |
|---|---|
| ⭐ [Read a product](#-read-a-product--cache-aside) | `GET /api/v1/products/{id}` |
| [List products](#list-products--deliberately-not-cached) | `GET /api/v1/products` |
| [Create](#create-a-listing) | `POST /api/v1/products` |
| ⭐ [Update](#-update--invalidate-never-rewrite) | `PATCH /api/v1/products/{id}` |
| [Delete](#delete) | `DELETE /api/v1/products/{id}` |
| ⭐ [Consume seller events](#-consuming-the-seller-stream) | Kafka `seller.events` |
| ⭐⭐ [Reserve stock](#-reserve-stock-grpc--mutual-tls) | gRPC `StockService/Reserve` |
| ⭐ [Confirm and the reaper](#-confirm-and-the-reaper) | gRPC `StockService/Confirm` |

---

## ⭐ Read a product — cache-aside

The cache is a **decorator implementing the `Repository` port**, not a branch
inside the service. The service is written as though no cache exists, and
removing it is one line in `main`.

Note the failure branch: when Redis is unavailable the request **falls through to
MongoDB** rather than failing. A cache that fails the request when it is down has
stopped being a cache and become a dependency — it has made availability worse,
which is the opposite of the reason it was added.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as product.Service
    participant Ch as rediscache decorator
    participant Rd as Redis
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/products/{id}
    H->>S: ByID(ctx, id)
    S->>Ch: ByID(ctx, id)
    Ch->>Rd: GET product:{id}
    alt hit
        Rd-->>Ch: cached JSON
        Ch->>Ch: increment the hit counter
        Ch-->>S: Product
    else miss or Redis is down
        Rd-->>Ch: nil, or a connection error
        Ch->>Ch: increment the miss counter
        Note over Ch,Rd: A Redis error falls through.<br/>It is a readiness signal, never a liveness one.
        Ch->>R: ByID(ctx, id)
        R->>DB: findOne by _id
        alt absent
            DB-->>R: ErrNoDocuments
            R-->>Ch: ErrProductNotFound
            Ch-->>S: ErrProductNotFound
            H-->>C: 404 product not found
        else found
            DB-->>R: document
            R-->>Ch: Product
            Ch->>Rd: SET product:{id} with TTL, best effort
            Ch-->>S: Product
        end
    end
    S-->>H: Product
    H-->>C: 200 product
```

## List products — deliberately not cached

Paged lists are **not** cached, and that is a decision rather than an omission:
every filter and page combination is a separate key, and one write would have to
invalidate an unknown number of them.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as product.Service
    participant Ch as rediscache decorator
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/products?seller_id=...&limit=20
    Note over C,H: Public. A catalogue that requires a login<br/>is a catalogue nobody browses.
    H->>S: List(ctx, filter, limit, offset)
    S->>Ch: List(...)
    Ch->>R: List(...) — passes straight through, no caching
    R->>DB: find using the seller_id + created_at index
    Note over R,DB: Compound and ordered to match the sort,<br/>so MongoDB walks the index instead of<br/>sorting in memory.
    DB-->>R: page and total
    R-->>Ch: products and total
    Ch-->>S: products and total
    S-->>H: products and total
    H-->>C: 200 products, total, limit, offset
```

## Create a listing

The shop must already be known **locally**. Product does not call Seller to find
out; it consults the read model built from the seller event stream. When the
event has not arrived yet the answer is **409, not 404** — because the account
may well have a shop and retrying is the right response, which a conflict says
and a not-found does not.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant MW as auth middleware
    participant H as handler
    participant S as product.Service
    participant D as seller_directory
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>MW: POST /api/v1/products with bearer token
    MW->>H: subject on the context
    H->>H: validate name, price_minor, currency, stock
    alt invalid
        H-->>C: 400 validation failed plus fields
    else valid
        H->>S: Create(ctx, NewProduct, userID)
        S->>D: ByUserID(ctx, subject)
        alt the seller event has not arrived yet
            D-->>S: not found
            S-->>H: ErrUnknownSeller
            H-->>C: 409 unknown seller — retry is reasonable
        else known
            D-->>S: SellerRef with id and name
            Note over S,D: SellerRef is Product's own small type.<br/>Sharing seller.Seller would couple the two<br/>and make them impossible to deploy apart.
            S->>S: denormalise seller_name onto the product
            S->>S: build the product.created event
            S->>R: Create(ctx, Product, events)
            R->>DB: transaction — insert product, insert outbox
            DB-->>R: commit
            R-->>S: Product
            H-->>C: 201 product
        end
    end
```

## ⭐ Update — invalidate, never rewrite

Two rules are visible here.

**Writes invalidate; they do not rewrite.** Rewriting the cached value lets two
racing writers leave the older one cached indefinitely — whoever writes to Redis
last wins, and that is not necessarily whoever wrote to MongoDB last.

**Invalidation is exact, never a scan.** The repository returns the affected IDs
so the decorator deletes precisely those keys. Scanning Redis for keys that might
match is `O(keyspace)` and gets slower exactly as the system gets busier.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as product.Service
    participant Ch as rediscache decorator
    participant R as mongodb adapter
    participant DB as MongoDB
    participant Rd as Redis

    C->>H: PATCH /api/v1/products/{id}
    H->>S: Update(ctx, id, Update, userID)
    S->>Ch: ByID for the ownership check
    Ch-->>S: Product
    alt the product belongs to another shop
        S-->>H: ErrNotOwner
        H-->>C: 403 this product belongs to another shop
    else owner
        S->>Ch: Update(ctx, id, Update, events)
        Ch->>R: Update(ctx, id, Update, events)
        R->>DB: transaction — update product, insert outbox
        DB-->>R: commit
        R-->>Ch: Product and the affected ids
        Ch->>Rd: DEL product:{id} — delete, not SET
        Note over Ch,Rd: Exactly the affected keys.<br/>Never SCAN, never a pattern.
        Ch-->>S: Product
        H-->>C: 200 product
    end
```

## Delete

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as product.Service
    participant Ch as rediscache decorator
    participant R as mongodb adapter
    participant Rd as Redis

    C->>H: DELETE /api/v1/products/{id}
    H->>S: Delete(ctx, id, userID)
    S->>Ch: ByID for the ownership check
    alt not the owner
        S-->>H: ErrNotOwner
        H-->>C: 403 forbidden
    else owner
        S->>Ch: Delete(ctx, id, events)
        Ch->>R: Delete plus outbox row, in one transaction
        R-->>Ch: ok
        Ch->>Rd: DEL product:{id}
        Ch-->>S: ok
        H-->>C: 204 no content
    end
```

## ⭐ Consuming the seller stream

Three rules that come from things that have already gone wrong elsewhere.

**Commit after handling, never before.** `FetchMessage` + `CommitMessages`
rather than `ReadMessage`, so a crash mid-handler replays the message instead of
losing it.

**Handlers must be idempotent.** Delivery is at-least-once and the commit happens
after the handler succeeds, so a repeat is certain, not possible.

**A permanently undecodable message returns nil**, not an error. Retrying it
forever blocks its partition for every message queued behind it.

```mermaid
sequenceDiagram
    autonumber
    participant K as Kafka seller.events
    participant Cn as kafka.Consumer
    participant Ev as product events handler
    participant S as product.Service
    participant D as seller_directory
    participant Ch as rediscache decorator
    participant Rd as Redis

    loop until the context is cancelled
        Cn->>K: FetchMessage
        K-->>Cn: message with headers and payload
        Cn->>Cn: extract the trace context from the headers
        Cn->>Ev: handle(ctx, key, value)
        alt payload cannot be decoded, ever
            Ev-->>Cn: nil — log it and move on
            Note over Ev,Cn: Returning an error here would retry it<br/>forever and block the partition.<br/>Production sends it to a dead-letter topic.
        else seller.registered
            Ev->>S: UpsertSeller(ctx, SellerRef)
            S->>D: upsert by seller id — idempotent by construction
        else seller.updated
            Ev->>S: RenameSeller(ctx, id, name)
            S->>D: upsert the directory entry
            S->>Ch: rewrite seller_name on that shop's products
            Ch->>Rd: DEL exactly the affected product keys
            Ch-->>S: affected ids
        end
        Ev-->>Cn: nil
        Cn->>K: CommitMessages — only now
    end
```

## ⭐⭐ Reserve stock (gRPC + mutual TLS)

The only synchronous call between services in the whole system, and the densest
diagram in this folder. Four separate ideas are load-bearing:

1. **Mutual TLS, not a bearer token.** There is no user in this call — the
   question is *which service is asking*, and only a client certificate answers
   that.
2. **The idempotency check happens outside the transaction.** Catching the
   duplicate-key error inside it and returning success asks the driver to commit
   a transaction that already contains a failed write; the commit fails with a
   retryable label, `WithTransaction` retries, the duplicate is still there, and
   the call spins until its context expires. That bug hung a test for 90 seconds.
3. **The decrement is a conditional update, not a lock.** `{_id, stock: {$gte: n}}`
   with `$inc` — the server matches and decrements in one step, so two buyers
   racing for the last unit cannot both succeed.
4. **The transaction is what makes it all-or-nothing across lines.** Per item the
   conditional update is already atomic; the transaction is there so a failure on
   the third line does not leave the first two decremented.

```mermaid
sequenceDiagram
    autonumber
    participant O as Order service
    participant T as TLS layer
    participant G as product gapi
    participant S as product.Service
    participant R as mongodb stock adapter
    participant DB as MongoDB

    O->>T: Reserve(items, idempotency_key)
    T->>T: present the client certificate, verify the server's
    alt no client certificate, or signed by another CA
        T-->>O: handshake failure — the RPC never reaches the server
        Note over O,T: RequireAndVerifyClientCert.<br/>Without it anything that can route<br/>a packet here could take inventory.
    else both certificates verify
        T->>G: Reserve
        G->>S: Reserve(ctx, key, items)
        S->>R: Reserve(ctx, key, items)

        R->>DB: findOne on stock_reservations by _id = key
        alt this key was already applied
            DB-->>R: the previous reservation
            R-->>S: the same lines as last time
            Note over R,DB: Outside any transaction, deliberately.<br/>See note 2 above.
        else new key
            R->>DB: start transaction
            R->>DB: insertOne on stock_reservations, _id = key
            loop for each item
                R->>DB: findOneAndUpdate {_id, stock: $gte n} with $inc -n
                alt no document matched
                    R->>DB: findOne to tell the two cases apart
                    alt the product does not exist
                        R-->>S: ErrProductNotFound
                    else it exists but has too little stock
                        R-->>S: ErrInsufficientStock
                    end
                    DB-->>R: abort — nothing is decremented
                end
            end
            R->>DB: record the reserved lines on the reservation
            DB-->>R: commit
        end
        R-->>S: reserved lines
        S-->>G: reserved lines
        alt ErrInsufficientStock
            G-->>O: FailedPrecondition
        else ErrProductNotFound
            G-->>O: NotFound
        else ok
            G-->>O: ReserveResponse
        end
    end
```

## ⭐ Confirm and the reaper

Two-phase reservation. `Reserve` takes the stock; `Confirm` says an order was
actually written against it. Anything still unconfirmed after the grace period
belongs to a caller that died between the two — and no amount of compensation
logic in that caller helps, because the caller is the thing that died.

The grace period is generous on purpose: reclaiming stock from an order that was
actually placed is a far worse failure than holding it a little longer.

```mermaid
sequenceDiagram
    autonumber
    participant O as Order service
    participant G as product gapi
    participant S as product.Service
    participant R as mongodb stock adapter
    participant DB as MongoDB
    participant Rp as reaper, every minute

    rect rgb(240, 248, 255)
        Note over O,DB: Phase two, on the happy path
        O->>G: Confirm(idempotency_key)
        G->>S: Confirm(ctx, key)
        S->>R: Confirm(ctx, key)
        R->>DB: updateByID set confirmed = true, no upsert
        Note over R,DB: No upsert: confirming a key that was<br/>never reserved should do nothing,<br/>not invent a reservation.
        DB-->>R: ok
        G-->>O: empty response
    end

    rect rgb(255, 245, 238)
        Note over Rp,DB: The safety net, when the caller died
        loop every minute
            Rp->>R: ReleaseExpired(ctx, 15 minutes)
            R->>DB: find confirmed=false, released=false, created_at < cutoff
            DB-->>R: at most 500 stale reservations
            loop for each one
                R->>DB: findOneAndUpdate {_id, released:false} set released=true
                Note over R,DB: Claim and flag in one step, so two<br/>concurrent releases cannot both<br/>proceed to add the stock back.
                alt already released or unknown
                    DB-->>R: no document — fine, skip it
                else claimed
                    loop for each reserved line
                        R->>DB: updateByID $inc stock +quantity
                    end
                end
            end
            R-->>Rp: count released
        end
    end
```
