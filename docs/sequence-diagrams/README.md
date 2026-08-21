# Sequence diagrams

*ภาษาไทย: [README.th.md](README.th.md)*

**42 diagrams per language, 84 in total, and every one of them parses** — checked against mermaid's own parser, not eyeballed.

Every endpoint in the system, drawn. Read this folder when you want to know what
actually happens between the request arriving and the response leaving —
including the parts that happen after the response has already left.

## How this is organised

Each domain has one file per language, following the repository's existing
convention (`x.md` / `x.th.md`). The Thai files are translations of the same
diagrams, not a different set — a diagram that changes must change in both.

| | Diagrams | EN | TH |
|---|---|---|---|
| **User** | 9 | [user.md](user.md) | [user.th.md](user.th.md) |
| **Seller** | 4 | [seller.md](seller.md) | [seller.th.md](seller.th.md) |
| ⭐ **Product** | 8 | [product.md](product.md) | [product.th.md](product.th.md) |
| ⭐⭐ **Order** | 5 | [order.md](order.md) | [order.th.md](order.th.md) |
| **Marketplace** | 3 | [marketplace.md](marketplace.md) | [marketplace.th.md](marketplace.th.md) |
| ⭐ **Live** | 6 | [live.md](live.md) | [live.th.md](live.th.md) |
| ⭐⭐ **Cross-cutting** | 7 | [cross-cutting.md](cross-cutting.md) | [cross-cutting.th.md](cross-cutting.th.md) |

⭐ marks the flows where the interesting engineering is, and ⭐⭐ the two files
worth reading if you only read two.

## If you only read four diagrams

These are the ones that are not obvious from the endpoint list, and the reason
the system is shaped the way it is.

1. **[Placing an order](order.md#-place-an-order--the-saga)** — two services, two
   databases, no transaction available. A saga: reserve, write, compensate.
2. **[The transactional outbox](cross-cutting.md#-the-outbox-and-its-relay)** —
   how an event gets published without a window in which the write succeeds and
   the publish does not.
3. **[Reserving stock](product.md#-reserve-stock-grpc--mutual-tls)** — one atomic
   conditional update instead of a lock, over gRPC secured by mutual TLS.
4. **[A trace across the outbox](cross-cutting.md#-a-trace-that-survives-the-outbox)**
   — the request has returned by the time the event is published, so the trace
   context is stored and replayed.

## Reading the notation

- `actor` is outside the system. `participant` is inside it.
- A solid arrow `->>` is a call; a dashed arrow `-->>` is its return.
- `alt` / `else` is a branch, `opt` is a branch that may not happen, `loop` is
  repetition, and `par` is genuine concurrency.
- Anything drawn **after** a response has been returned to the client is work
  that happens on a background loop. That distinction is the whole subject of
  the outbox diagrams, so it is drawn rather than described.

## Keeping them true

A diagram that disagrees with the code is worse than no diagram, because it is
believed. These were drawn from the code as it is, not from the design as it was
intended, and the endpoint list matches
[`../../postman/`](../../postman/) — which is executable, so it cannot quietly
drift.

Related: [domains.md](../domains.md) for the topology,
[tech-stack.md](../tech-stack.md) for why each technology is here,
[user-domain-design.md](../user-domain-design.md) for the user stories the User
diagrams came from, and
[transactional-outbox.md](../transactional-outbox.md) for the outbox in prose.
