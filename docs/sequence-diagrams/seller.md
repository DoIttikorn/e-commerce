# Seller — sequence diagrams

*ภาษาไทย: [seller.th.md](seller.th.md) · Index: [README.md](README.md)*

Shops. Every write here publishes an event that Product and Marketplace consume
— this is where the event-driven half of the system starts.

| Flow | Endpoint |
|---|---|
| [Open a shop](#open-a-shop) | `POST /api/v1/sellers` |
| [My shop](#my-shop) | `GET /api/v1/sellers/me` |
| [List and get](#list-and-get) | `GET /api/v1/sellers`, `GET /api/v1/sellers/{id}` |
| ⭐ [Rename a shop](#-rename-a-shop--the-one-worth-running) | `PATCH /api/v1/sellers/{id}` |

---

## Open a shop

The shop row and its event are written in **one transaction**. The event is not
published here — a background relay does that afterwards. Publishing from the
request path leaves a window: the write commits, the publish fails, and the shop
exists while nobody was told. Returning an error would not help, because the
write already happened.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant MW as auth middleware
    participant H as handler
    participant S as seller.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>MW: POST /api/v1/sellers with bearer token
    MW->>H: subject on the context
    H->>H: validate shop_name, trim it
    alt invalid
        H-->>C: 400 validation failed plus fields
    else valid
        H->>S: Register(ctx, NewSeller)
        S->>S: build the seller.registered event
        S->>R: Create(ctx, Seller, events)
        R->>DB: start transaction
        R->>DB: insertOne on sellers
        R->>DB: insertMany on outbox
        alt either write fails
            DB-->>R: abort, nothing is committed
            R-->>S: error
            H-->>C: 409 or 500, and no event exists
        else both succeed
            DB-->>R: commit
            R-->>S: Seller
            S-->>H: Seller
            H-->>C: 201 seller, status active
        end
    end
    Note over R,DB: The event is now durable and unpublished.<br/>See cross-cutting.md for the relay<br/>that picks it up.
```

## My shop

Resolves the shop from the token's subject rather than a path parameter. There is
no ID to get wrong, and no way to ask for somebody else's.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant MW as auth middleware
    participant H as handler
    participant S as seller.Service
    participant R as mongodb adapter

    C->>MW: GET /api/v1/sellers/me
    MW->>H: subject on the context
    H->>S: ByUserID(ctx, subject)
    S->>R: ByUserID(ctx, userID)
    alt this account has no shop
        R-->>S: ErrSellerNotFound
        H-->>C: 404 seller not found
    else found
        R-->>S: Seller
        S-->>H: Seller
        H-->>C: 200 seller
    end
```

## List and get

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as seller.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/sellers or /api/v1/sellers/{id}
    alt list
        H->>S: List(ctx, limit, offset)
        S->>R: List
        R->>DB: find plus countDocuments
        DB-->>R: page and total
        H-->>C: 200 sellers, total, limit, offset
    else single
        H->>S: ByID(ctx, id)
        S->>R: ByID
        alt malformed id
            H-->>C: 400 malformed seller id
        else absent
            H-->>C: 404 seller not found
        else found
            H-->>C: 200 seller
        end
    end
```

## ⭐ Rename a shop — the one worth running

This is the diagram that shows the architecture. Renaming a shop changes rows in
**two other services** without either of them being called.

Product never calls Seller. It keeps its own read model built from this stream,
so rendering a listing page costs zero outbound calls — and a Seller service that
is down does not stop the catalogue from rendering.

The event is keyed by seller ID because Kafka orders **per partition**: two
renames of one shop must share a key or they can be applied backwards.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as seller.Service
    participant R as mongodb adapter
    participant DB as MongoDB
    participant RL as outbox relay
    participant K as Kafka
    participant P as Product service
    participant M as Marketplace service

    C->>H: PATCH /api/v1/sellers/{id}
    H->>H: requireOwner, subject must own the shop
    alt not the owner
        H-->>C: 403 forbidden
    else owner
        H->>S: Update(ctx, id, Update)
        S->>S: build seller.updated, key = seller id
        S->>R: Update(ctx, id, Update, events)
        R->>DB: transaction, update sellers and insert outbox
        DB-->>R: commit
        R-->>S: Seller
        H-->>C: 200 seller
    end

    Note over C,H: The request is over. Everything below<br/>happens on a background loop.

    RL->>DB: claim the oldest unpublished row
    RL->>K: publish to seller.events, key = seller id
    RL->>DB: mark published_at
    par every consumer of the topic
        K->>P: seller.updated
        P->>P: upsert the local seller_directory
        P->>P: rewrite seller_name on that shop's products
        P->>P: invalidate exactly those cache keys, never a scan
    and
        K->>M: seller.updated
        M->>M: update seller_name on the search projection
    end
    Note over P,M: A listing page in either service now shows<br/>the new name, and neither ever called Seller.
```
