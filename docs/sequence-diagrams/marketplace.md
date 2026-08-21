# Marketplace — sequence diagrams

*ภาษาไทย: [marketplace.th.md](marketplace.th.md) · Index: [README.md](README.md)*

A search projection fed by **three** event streams. It owns no writes at all:
every row is the result of an event somebody else published, which is why it has
exactly one endpoint and three consumers.

| Flow | Endpoint |
|---|---|
| ⭐ [Search](#-search--projection-plus-ttl-cache) | `GET /api/v1/marketplace/listings` |
| ⭐ [Three streams, one projection](#-three-streams-one-projection) | Kafka |
| [Building the text index](#building-the-text-index) | startup |

---

## ⭐ Search — projection plus TTL cache

Two things are worth noticing.

**The projection is why this is one query.** Answering "mugs under ฿500 from
active shops, best selling first" from the source data would mean joining three
services. Here it is a single indexed find, because the joining already happened
when the events arrived.

**The cache is a TTL, not an invalidation scheme.** A search cache is a ceiling
on staleness rather than a correctness mechanism: nobody minds a listing
appearing a few seconds late, and the alternative — invalidating every query that
might match a changed product — is not computable.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as marketplace handler
    participant S as marketplace.Service
    participant Ch as rediscache decorator
    participant Rd as Redis
    participant R as mongodb adapter
    participant DB as MongoDB marketplace

    C->>H: GET /api/v1/marketplace/listings?q=mug&sort=best_selling
    Note over C,H: Public. No token.
    H->>H: parse and clamp q, price bounds, in_stock, sort, paging
    alt sort is not one of the known values
        H-->>C: 400 validation failed plus fields
    else valid
        H->>S: Search(ctx, Query)
        S->>Ch: Search(ctx, Query)
        Ch->>Ch: build a cache key from the whole normalised query
        Ch->>Rd: GET search:{hash}
        alt hit
            Rd-->>Ch: cached page
            Ch-->>S: listings and total
        else miss or Redis unavailable
            Note over Ch,Rd: Falls through to MongoDB.<br/>Redis is a readiness signal here,<br/>never a liveness one.
            Ch->>R: Search(ctx, Query)
            alt q is present
                R->>DB: find with $text plus the filters, sorted by textScore
                Note over R,DB: A real text index — it stems and ranks.<br/>Not a substring match.
            else no q
                R->>DB: find with the filters, sorted by the requested field
            end
            DB-->>R: listings and total
            R-->>Ch: listings and total
            Ch->>Rd: SET search:{hash} with a short TTL
            Ch-->>S: listings and total
        end
        S-->>H: listings and total
        H-->>C: 200 listings, total, limit, offset, sort
    end
```

## ⭐ Three streams, one projection

Nothing in Seller, Product or Order knows the Marketplace exists. They publish
what happened; whoever cares subscribes. That is the difference between an event
and a call: adding a seventh consumer requires no change to any publisher.

Every handler is idempotent, because at-least-once delivery makes a repeat
certain rather than possible.

```mermaid
sequenceDiagram
    autonumber
    participant KS as Kafka seller.events
    participant KP as Kafka product.events
    participant KO as Kafka order.events
    participant Cn as three consumers, one group each
    participant S as marketplace.Service
    participant DB as MongoDB marketplace

    par the three streams run independently
        KS->>Cn: seller.registered or seller.updated
        Cn->>S: UpsertSeller / RenameSeller
        S->>DB: upsert seller_name across that shop's listings
        Note over S,DB: A shop rename reaches a third service<br/>that has never called the second.
    and
        KP->>Cn: product.created, product.updated, product.deleted
        Cn->>S: UpsertListing or RemoveListing
        S->>DB: upsert or delete by product_id
        Note over S,DB: Keyed by product id, so the write is<br/>idempotent however many times<br/>the same event arrives.
    and
        KO->>Cn: order.paid
        Cn->>S: RecordSale(ctx, lines)
        loop for each line
            S->>DB: $inc sold_count by the quantity
        end
        Note over S,DB: This is what makes sort=best_selling<br/>possible without asking Order anything.
    end
    Cn->>Cn: commit each offset only after its handler succeeded
```

## Building the text index

Index creation belongs to the domain's own adapter, not to a shared database
package — so adding a domain never means editing `internal/database`.

```mermaid
sequenceDiagram
    autonumber
    participant M as cmd/marketplace main
    participant A as appserver
    participant R as mongodb adapter
    participant DB as MongoDB marketplace
    participant K as Kafka

    M->>A: New(ctx, "marketplace")
    A->>DB: connect and ping
    M->>R: EnsureIndexes(ctx)
    R->>DB: create the text index over name and seller_name
    R->>DB: create the filter indexes for price and sold_count
    R->>DB: create the outbox indexes
    M->>K: EnsureTopic for each topic it consumes
    Note over M,K: Created at startup rather than relying on<br/>auto-creation: a consumer that subscribes<br/>before the first publish otherwise races<br/>topic creation and sits idle.
    M->>A: start three consumers as background tasks
    M->>A: Run — serve until a signal arrives
```
