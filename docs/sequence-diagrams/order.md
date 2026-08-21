# Order — sequence diagrams

*ภาษาไทย: [order.th.md](order.th.md) · Index: [README.md](README.md)*

The saga. Placing an order spans two services with two separate databases, so
there is **no transaction available** — anything spanning both is a sequence of
local steps plus a compensating action for when a later one fails.

| Flow | Endpoint |
|---|---|
| ⭐⭐ [Place an order](#-place-an-order--the-saga) | `POST /api/v1/orders` |
| ⭐ [Replaying an idempotency key](#-replaying-an-idempotency-key) | `POST /api/v1/orders` |
| [List and get](#list-and-get) | `GET /api/v1/orders`, `GET /api/v1/orders/{id}` |
| [Pay](#pay) | `POST /api/v1/orders/{id}/pay` |
| ⭐ [Cancel](#-cancel--the-compensating-action) | `POST /api/v1/orders/{id}/cancel` |

---

## ⭐⭐ Place an order — the saga

The whole system in one request, and the diagram worth reading twice.

**Why this one call is synchronous.** Every other cross-service interaction here
is an event, because every other one can wait. This one cannot: a buyer must not
be told their order exists until the stock is actually secured.

**Why `Confirm` failing does not fail the request.** By then the order is
written and the buyer has been told. The reservation is merely left unconfirmed,
and the reaper's grace period is long enough that a retry or a restart fixes it.
Failing the request here would report a success as a failure.

**Why the compensation is best-effort.** If `Release` also fails, the reaper
still reclaims the stock. Two independent mechanisms cover the same gap because
the expensive failure — stock held against nothing, forever — is worth covering
twice.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as order handler
    participant S as order.Service
    participant Cl as grpcstock client
    participant P as Product service
    participant R as mongodb adapter
    participant DB as MongoDB order
    participant RL as outbox relay
    participant K as Kafka

    C->>H: POST /api/v1/orders with Idempotency-Key
    alt header missing
        H-->>C: 400 — the key is what makes a retry safe
    else present
        H->>H: validate the items
        H->>S: Place(ctx, buyerID, items, key)

        rect rgb(255, 250, 235)
            Note over S,P: Step 1 — reserve, over gRPC and mutual TLS
            S->>Cl: Reserve(ctx, key, lines)
            Cl->>P: StockService/Reserve
            alt insufficient stock or unknown product
                P-->>Cl: FailedPrecondition or NotFound
                Cl-->>S: mapped domain error
                S-->>H: error
                H-->>C: 409 or 404 — no order was created
            else reserved
                P-->>Cl: reserved lines with authoritative prices
                Cl-->>S: reserved lines
                Note over S,Cl: The price comes from Product, never<br/>from the client. A client-supplied price<br/>is a client-supplied discount.
            end
        end

        rect rgb(240, 255, 240)
            Note over S,DB: Step 2 — write the order and its event together
            S->>S: total from the reserved lines, minor units only
            S->>R: Save(ctx, Order, events)
            R->>DB: transaction — insert order, insert outbox
            alt the write fails
                DB-->>R: abort
                R-->>S: error
                rect rgb(255, 235, 235)
                    Note over S,P: Compensate — the stock is held<br/>against an order that does not exist
                    S->>Cl: Release(ctx, key)
                    Cl->>P: release the reservation
                    Note over S,P: Best effort. If this fails too,<br/>the reaper still reclaims it.
                end
                S-->>H: error
                H-->>C: 500 — and no stock is stranded
            else committed
                DB-->>R: commit
                R-->>S: Order
            end
        end

        rect rgb(240, 248, 255)
            Note over S,P: Step 3 — confirm, so the reaper leaves it alone
            S->>Cl: Confirm(ctx, key)
            Cl->>P: StockService/Confirm
            alt confirm fails
                Note over S,Cl: Logged, not returned. The order exists<br/>and the buyer has been told — the reaper's<br/>grace period covers the rest.
            end
        end

        S-->>H: Order
        H-->>C: 201 order, status pending
    end

    Note over C,H: The request is over.

    RL->>DB: claim the outbox row
    RL->>K: publish order.events, key = order id
    RL->>DB: mark published_at
    Note over RL,K: Marketplace consumes this to maintain<br/>sold_count. Order knows nothing about it.
```

## ⭐ Replaying an idempotency key

What a retrying client does after a timeout. The caller cannot tell whether the
server acted, so it will retry — and the key is what makes that retry safe rather
than turning "reserve stock" into "reserve stock twice".

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant S as order.Service
    participant Cl as grpcstock client
    participant P as Product service
    participant R as mongodb adapter
    participant DB as MongoDB order

    Note over C: The first attempt timed out.<br/>Did the server act? Unknowable.
    C->>S: POST /api/v1/orders, same Idempotency-Key
    S->>R: find an order already written under this key
    alt the order exists
        R->>DB: findOne by idempotency key
        DB-->>R: the original order
        R-->>S: Order
        S-->>C: 200 with the SAME order — no second write
    else no order yet
        S->>Cl: Reserve(ctx, key, lines)
        Cl->>P: StockService/Reserve
        P->>P: this key is already in stock_reservations
        P-->>Cl: the SAME reserved lines as the first attempt
        Note over P,Cl: Stock is decremented once, not twice.<br/>Without the key a flaky network<br/>becomes double-charged customers.
        Cl-->>S: reserved lines
        S->>R: Save — the order is written this time
        S-->>C: 201 order
    end
```

## List and get

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant MW as auth middleware
    participant H as order handler
    participant S as order.Service
    participant R as mongodb adapter

    C->>MW: GET /api/v1/orders or /api/v1/orders/{id}
    MW->>H: subject on the context
    alt list
        H->>S: ListForBuyer(ctx, subject, limit, offset)
        Note over H,S: Scoped to the subject. There is no<br/>"all orders" endpoint, so omitting a<br/>filter cannot widen the result.
        S->>R: list by buyer_user_id
        R-->>S: orders and total
        H-->>C: 200 orders, total, limit, offset
    else single
        H->>S: ByID(ctx, id)
        S->>R: ByID
        alt malformed id
            H-->>C: 400 malformed order id
        else absent
            H-->>C: 404 order not found
        else belongs to another buyer
            H-->>C: 403 forbidden
        else own order
            H-->>C: 200 order
        end
    end
```

## Pay

The reserved stock stays reserved — the sale completed. A state machine guards
the transition, so paying a cancelled order is a conflict rather than a
correction.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as order handler
    participant S as order.Service
    participant R as mongodb adapter
    participant DB as MongoDB order

    C->>H: POST /api/v1/orders/{id}/pay
    H->>S: Pay(ctx, id, buyerID)
    S->>R: ByID(ctx, id)
    alt not the buyer
        S-->>H: ErrNotBuyer
        H-->>C: 403 forbidden
    else buyer
        alt status is not pending
            S-->>H: ErrInvalidTransition
            H-->>C: 409 — a paid or cancelled order cannot be paid
        else pending
            S->>R: Save with status paid, plus the order.paid outbox event
            R->>DB: transaction — update order, insert outbox
            DB-->>R: commit
            S-->>H: Order
            H-->>C: 200 order, status paid
        end
    end
```

## ⭐ Cancel — the compensating action

The other end of the saga. Cancelling releases the reservation and the stock
returns.

**A compensating action must be safe to call repeatedly**, because it runs on
exactly the path where retries happen. Releasing an unknown key, or one already
released, returns success rather than an error — refusing a repeat here would
turn a recoverable situation into a stuck one.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as order handler
    participant S as order.Service
    participant R as mongodb adapter
    participant DB as MongoDB order
    participant Cl as grpcstock client
    participant P as Product service
    participant PD as MongoDB product

    C->>H: POST /api/v1/orders/{id}/cancel
    H->>S: Cancel(ctx, id, buyerID)
    S->>R: ByID(ctx, id)
    alt not the buyer
        H-->>C: 403 forbidden
    else status is not pending
        H-->>C: 409 — already paid or already cancelled
    else pending and owned
        S->>R: Save with status cancelled, plus the order.cancelled event
        R->>DB: transaction — update order, insert outbox
        DB-->>R: commit

        S->>Cl: Release(ctx, idempotency key)
        Cl->>P: release the reservation
        P->>PD: findOneAndUpdate {_id, released:false} set released=true
        alt unknown key, or already released
            PD-->>P: no document
            P-->>Cl: success anyway
            Note over P,Cl: Idempotent on purpose. This runs on the<br/>retry path, so refusing a repeat would<br/>turn recoverable into stuck.
        else claimed
            loop for each reserved line
                P->>PD: $inc stock by +quantity
            end
            P-->>Cl: success
        end
        Cl-->>S: ok
        S-->>H: Order
        H-->>C: 200 order, status cancelled
    end
```
