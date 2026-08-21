# Live Commerce — sequence diagrams

*ภาษาไทย: [live.th.md](live.th.md) · Index: [README.md](README.md)*

Live selling, and every design decision in it follows from one fact: **a
WebSocket lives on exactly one instance**. Anything that has to be true for all
viewers therefore cannot live in process memory.

Redis is **required** here, unlike in Product where it is only a cache. Two
instances without it would each have their own idea of the audience, and both
would be wrong.

| Flow | Endpoint |
|---|---|
| [Create a stream](#create-a-stream) | `POST /api/v1/live/streams` |
| [Start and end](#start-and-end) | `POST .../start`, `POST .../end` |
| ⭐⭐ [Watch — the WebSocket](#-watch--the-websocket) | `GET .../watch` |
| ⭐ [Feature a product](#-feature-a-product--cross-instance-broadcast) | `POST .../feature` |
| ⭐ [Presence that expires](#-presence-that-expires) | background |
| [List and get](#list-and-get) | `GET /api/v1/live/streams`, `GET .../{id}` |

---

## Create a stream

```mermaid
sequenceDiagram
    autonumber
    actor C as Host
    participant MW as auth middleware
    participant H as live handler
    participant S as live.Service
    participant D as seller_directory
    participant R as mongodb adapter

    C->>MW: POST /api/v1/live/streams with bearer token
    MW->>H: subject on the context
    H->>H: validate the title
    H->>S: Create(ctx, title, userID)
    S->>D: ByUserID(ctx, subject)
    alt no shop, or the seller event has not arrived
        D-->>S: not found
        H-->>C: 409 unknown seller
    else known
        D-->>S: SellerRef
        S->>R: Create(ctx, Stream)
        R-->>S: Stream, status scheduled
        H-->>C: 201 stream
    end
```

## Start and end

```mermaid
sequenceDiagram
    autonumber
    actor C as Host
    participant H as live handler
    participant S as live.Service
    participant R as mongodb adapter
    participant B as redisbus

    C->>H: POST /api/v1/live/streams/{id}/start or /end
    H->>S: Start or End(ctx, id, userID)
    S->>R: ByID(ctx, id)
    alt not the host
        S-->>H: ErrNotHost
        H-->>C: 403 forbidden
    else host
        alt the transition is not legal from this status
            S-->>H: ErrInvalidTransition
            H-->>C: 409 conflict
        else legal
            S->>R: update status and stamp started_at or ended_at
            S->>B: publish stream.started or stream.ended
            Note over S,B: Redis pub/sub, so viewers on every<br/>instance are told, not just the ones<br/>connected to this process.
            H-->>C: 200 stream
        end
    end
```

## ⭐⭐ Watch — the WebSocket

The densest diagram here, and three of its details are the kind that only show up
once a real client connects.

**Read from the socket even when the client sends nothing.** A WebSocket close
only surfaces through a read — a handler that never reads never learns the viewer
left, and the presence entry lingers until it expires.

**Send a snapshot, not a join event.** The first version sent `viewer.joined`
directly to the new socket *and* let it arrive again from the broadcast it had
just subscribed to. Every viewer saw the message twice. It is now a `stream.state`
snapshot, which is both more useful and not a duplicate.

**Drop frames for a slow viewer rather than blocking.** One stalled connection
must not hold up everyone sharing the process.

```mermaid
sequenceDiagram
    autonumber
    actor V as Viewer
    participant H as live handler
    participant S as live.Service
    participant Rd as Redis
    participant B as redisbus subscription

    V->>H: GET /api/v1/live/streams/{id}/watch with Upgrade
    alt the stream does not exist
        H-->>V: 404 before the upgrade
    else exists
        H->>H: accept the WebSocket upgrade
        H->>Rd: ZADD presence:{id} score=now member=viewerID
        H->>B: subscribe to stream:{id}
        H->>S: current state and viewer count
        H-->>V: stream.state snapshot — status, featured product, viewers
        Note over H,V: A snapshot, not a viewer.joined.<br/>The join arrives once, from the broadcast.

        par three things run concurrently for the life of the socket
            loop until the socket closes
                B-->>H: a broadcast frame from any instance
                alt the viewer's send buffer is full
                    H->>H: drop the frame and count it
                    Note over H: A slow viewer must not stall<br/>everyone else in this process.
                else room to send
                    H-->>V: the frame
                end
            end
        and
            loop every heartbeat interval
                H->>Rd: ZADD presence:{id} refresh this viewer's score
                Note over H,Rd: Nobody sends a goodbye when a laptop<br/>lid closes, so presence must expire<br/>rather than be deleted on exit.
            end
        and
            loop until the socket closes
                H->>V: read
                Note over H,V: Read even though the client sends nothing.<br/>A close is only visible through a read.
            end
        end

        V--xH: the connection closes
        H->>B: unsubscribe
        H->>Rd: ZREM presence:{id} this viewer
    end
```

## ⭐ Feature a product — cross-instance broadcast

This is the diagram that shows why Redis is required rather than optional. The
host is connected to one instance; the viewers are spread across all of them.

**Redis pub/sub, not Kafka**, and deliberately: it has no replay, which is
correct for a feed whose value decays in seconds and wrong for anything durable.
This is also the one publisher in the system with **no outbox**, for the same
reason — a delivery guarantee for a notification that is worthless when stale is
machinery in service of nothing.

```mermaid
sequenceDiagram
    autonumber
    actor C as Host
    participant H1 as live instance 1
    participant S as live.Service
    participant R as mongodb adapter
    participant Rd as Redis pub/sub
    participant H2 as live instance 2
    actor V1 as Viewer on instance 1
    actor V2 as Viewer on instance 2

    C->>H1: POST /api/v1/live/streams/{id}/feature
    H1->>S: Feature(ctx, id, productID, userID)
    alt not the host
        H1-->>C: 403 forbidden
    else host
        S->>R: set featured_product_id
        R-->>S: Stream
        S->>Rd: PUBLISH stream:{id} product.featured
        H1-->>C: 200 stream
    end

    par Redis fans the message out to every subscriber
        Rd-->>H1: product.featured
        H1-->>V1: product.featured
    and
        Rd-->>H2: product.featured
        H2-->>V2: product.featured
    end
    Note over V1,V2: Both viewers see it. Without Redis,<br/>only the ones on instance 1 would —<br/>and instance 2 would not know it happened.
```

## ⭐ Presence that expires

Viewer counts are a sorted set scored by timestamp, pruned on read. The reason is
blunt: **nobody sends a goodbye when a laptop lid closes.** A design that deletes
on disconnect counts a viewer forever the first time a connection dies rudely,
and connections die rudely all the time.

```mermaid
sequenceDiagram
    autonumber
    participant H as live handler
    participant Rd as Redis sorted set
    actor V as Viewer

    rect rgb(240, 255, 240)
        Note over V,Rd: While the socket is open
        V->>H: connected
        H->>Rd: ZADD presence:{id} now viewerID
        loop every heartbeat
            H->>Rd: ZADD refresh the score to now
        end
    end

    rect rgb(255, 245, 238)
        Note over V,Rd: The rude disconnect — no close frame ever arrives
        V--xH: laptop lid closes, network drops
        Note over H: The handler's read eventually errors,<br/>but it may take a while.
    end

    rect rgb(240, 248, 255)
        Note over H,Rd: Any read of the viewer count
        H->>Rd: ZREMRANGEBYSCORE presence:{id} 0 (now - ttl)
        Note over H,Rd: Prune first. Anyone who has not<br/>heartbeated recently is gone,<br/>whether or not they said so.
        H->>Rd: ZCARD presence:{id}
        Rd-->>H: the count of viewers actually still there
    end
```

## List and get

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as live handler
    participant S as live.Service
    participant R as mongodb adapter
    participant Rd as Redis

    C->>H: GET /api/v1/live/streams or /api/v1/live/streams/{id}
    Note over C,H: Public — no token needed to browse<br/>what is on air.
    alt list
        H->>S: List(ctx, limit, offset)
        S->>R: find plus count
        H-->>C: 200 streams, total, limit, offset
    else single
        H->>S: ByID(ctx, id)
        S->>R: ByID
        alt absent
            H-->>C: 404 stream not found
        else found
            S->>Rd: prune, then ZCARD presence:{id}
            Rd-->>S: current viewer count
            H-->>C: 200 stream with the live viewer count
        end
    end
```
