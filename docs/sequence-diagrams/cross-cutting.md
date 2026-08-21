# Cross-cutting — sequence diagrams

*ภาษาไทย: [cross-cutting.th.md](cross-cutting.th.md) · Index: [README.md](README.md)*

The machinery every service shares. None of it belongs to a domain, and all of it
is the reason the domains can stay as simple as they are.

| Flow | Where |
|---|---|
| ⭐⭐ [The outbox and its relay](#-the-outbox-and-its-relay) | `internal/outbox` |
| ⭐⭐ [A trace that survives the outbox](#-a-trace-that-survives-the-outbox) | `internal/tracing` |
| ⭐ [The middleware chain](#-the-middleware-chain) | `internal/middleware` |
| ⭐ [The mutual TLS handshake](#-the-mutual-tls-handshake) | `internal/servicetls` |
| [Liveness and readiness](#liveness-and-readiness) | `internal/appserver` |
| [Startup](#startup) | `internal/appserver` |
| [Graceful shutdown](#graceful-shutdown) | `internal/appserver` |

---

## ⭐⭐ The outbox and its relay

*In prose: [transactional-outbox.md](../transactional-outbox.md)*

The problem this exists to remove, drawn as the top half of the diagram: a
service that writes and then publishes has a **window**. Crash in it, or have the
broker refuse, and the change is real while nobody was told. Returning an error
does not help — the write has committed — so the event is simply lost, and the
systems that needed it drift apart with nothing to indicate it happened.

Writing the event into the same transaction removes the window entirely. What
remains is publishing it afterwards, which is allowed to be slow, retried, and
duplicated. The trade is **at-least-once instead of at-most-once**, which is the
right way round: consumers must cope with a repeat anyway, because Kafka gives
them the same guarantee.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant S as any domain service
    participant R as mongodb adapter
    participant DB as MongoDB
    participant RL as outbox relay
    participant K as Kafka

    rect rgb(255, 235, 235)
        Note over C,K: What this replaces — publish from the request path
        C->>S: a write request
        S->>DB: commit the change
        S--xK: publish fails, or the process dies here
        Note over S,K: The change is real. Nobody was told.<br/>Failing the request would report<br/>a success as a failure.
    end

    rect rgb(240, 255, 240)
        Note over C,DB: What happens instead
        C->>S: a write request
        S->>S: build the event alongside the change
        S->>R: Save(ctx, entity, events)
        R->>DB: start transaction
        R->>DB: write the entity
        R->>DB: insert the events into the outbox
        alt anything fails
            DB-->>R: abort — no change, and no event
            Note over R,DB: The two cannot disagree.<br/>That is the whole point.
        else
            DB-->>R: commit — both, or neither
        end
        S-->>C: response
    end

    rect rgb(240, 248, 255)
        Note over RL,K: The relay, on a background loop
        loop forever
            RL->>DB: findOneAndUpdate oldest unpublished, set claimed_at
            Note over RL,DB: A lease, so two relays do not<br/>publish the same row at once.
            alt nothing pending
                RL->>RL: sleep one second, then look again
            else claimed a row
                RL->>K: PublishRaw topic, key, the stored bytes
                Note over RL,K: Raw. Re-encoding here would let a later<br/>change to the event type quietly rewrite<br/>events recorded before that change.
                alt the broker is down
                    K--xRL: error
                    Note over RL,K: Left claimed. The lease expires and it is<br/>retried — which is why a broker outage<br/>delays delivery instead of losing it.
                else published
                    RL->>DB: set published_at
                    Note over RL,DB: Published but not marked is possible,<br/>and republishes later. That is the<br/>at-least-once edge, and why keys matter.
                end
            end
        end
    end
```

## ⭐⭐ A trace that survives the outbox

A request ID correlates log lines **within** one process. It does not survive a
gRPC call, and it certainly does not survive an event written during a request
and published a second after that request returned.

This is the diagram of the hard part. The span that wrote the event has already
ended by the time the relay runs, so the trace context is **stored in the outbox
row** and replayed. Parenting to an ended span is deliberate and legal: a trace
is a causal chain, not a call stack.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant MW as tracing middleware
    participant S as domain service
    participant O as outbox.Append
    participant DB as MongoDB
    participant RL as outbox relay
    participant K as Kafka
    participant Cn as consumer in another service

    C->>MW: request, possibly with a traceparent header
    MW->>MW: extract — join the caller's trace rather than start a new one
    Note over MW: A service that drops an inbound trace context<br/>puts a hole in somebody else's trace.
    MW->>MW: start a server span
    MW->>S: handle with the span on the context

    S->>O: Append(ctx, events)
    O->>O: inject the ambient trace context into a carrier
    O->>DB: insert the outbox rows, trace_context included
    Note over O,DB: Stored, because in a moment this context<br/>will no longer exist anywhere.

    S-->>MW: done
    MW->>MW: rename the span to METHOD /route-pattern
    Note over MW: Renamed after the router matched, so the name<br/>is "GET /api/v1/users/{id}" and not one span<br/>name per user id. Same cardinality rule as<br/>the Prometheus labels — which is why this<br/>middleware is hand-written, not otelhttp.
    MW-->>C: response
    Note over C,MW: The request is over. Its context is gone.

    RL->>DB: claim a row, trace_context and all
    RL->>RL: extract that context back out
    RL->>RL: start a producer span parented to the original request
    Note over RL: Its parent ended seconds ago. Legal, and<br/>what every Kafka instrumentation does.<br/>Carries outbox.lag_ms — how long it waited.
    RL->>K: publish, injecting traceparent into the message HEADERS
    Note over RL,K: Headers, never the payload. The payload is<br/>the domain's contract — a consumer that knows<br/>nothing about tracing still parses it.

    K->>Cn: the message, minutes later, in another process
    Cn->>Cn: extract from the headers, start a consumer span
    Note over Cn: One trace now spans the checkout, the<br/>publish, and the projection update —<br/>instead of three unrelated log streams<br/>and a timestamp comparison.
```

## ⭐ The middleware chain

Order is not arbitrary. **RequestID must be first**, or everything downstream
records uncorrelated lines. **Tracing must precede Logging**, so `trace_id` is on
the context before anything logs.

Correlation happens in the **slog handler**, not at call sites: any code that
logs with the request context is correlated for free, including domain code that
knows nothing about HTTP. Threading a logger through function arguments to
achieve the same thing is the alternative, and it is worse.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant ID as RequestID
    participant T as Tracing
    participant M as Metrics
    participant L as Logging
    participant Rt as chi router
    participant H as handler

    C->>ID: request
    ID->>ID: reuse X-Request-ID, or generate one
    Note over ID: An inbound value over 64 bytes or with<br/>non-printable ASCII is discarded —<br/>it ends up in logs, so it is a<br/>log-forging vector.
    ID->>T: ctx carries the request id
    T->>T: extract traceparent, start a server span
    T->>M: ctx carries the trace too
    M->>M: start the timer, prepare the pattern holder
    M->>L: onward
    L->>Rt: onward
    Rt->>Rt: match the route, SetPattern on the request
    Rt->>H: the handler runs
    H-->>Rt: status and body
    Rt-->>L: unwinding
    L->>L: log method, path, status, duration, with request_id and trace_id
    L-->>M: unwinding
    M->>M: label by ROUTE PATTERN, never r.URL.Path
    Note over M: A raw path mints one time series per id.<br/>Unmatched requests collapse to "unmatched"<br/>so a scanner cannot do the same.
    M-->>T: unwinding
    T->>T: rename the span to the route pattern, set the status
    Note over T: Only 5xx marks the span an error.<br/>A 404 or a 422 is the server working.
    T-->>ID: unwinding
    ID-->>C: response, X-Request-ID echoed back
```

## ⭐ The mutual TLS handshake

Service-to-service authentication is not the same problem as user
authentication, and a user's bearer token is the wrong tool for it: there is no
user when Order reserves stock, and borrowing somebody's token would mean a
compromised Order service can act as whichever buyer happened to be checking out.

```mermaid
sequenceDiagram
    autonumber
    participant O as Order, the client
    participant OT as client TLS
    participant PT as server TLS
    participant P as Product, the server

    O->>OT: dial product:9090
    OT->>PT: ClientHello, TLS 1.3 minimum
    PT-->>OT: ServerHello, server certificate, certificate request
    OT->>OT: verify the server against a dedicated CA pool
    Note over OT: A fresh x509.NewCertPool, never the system<br/>roots. Any public CA being trusted here<br/>would defeat the entire exercise.
    alt the server name is not in the certificate
        OT--xPT: abort — this is not who it claims to be
    else the name matches
        OT->>PT: the client certificate, CN = order
        PT->>PT: RequireAndVerifyClientCert against the same CA
        alt no certificate presented
            PT--xOT: handshake failure
            Note over PT: Without this, anything that can route a<br/>packet to the port could reserve stock.
        else signed by an unrelated CA
            PT--xOT: handshake failure
        else valid
            PT->>P: the RPC finally reaches application code
            P-->>O: response over the established channel
        end
    end
```

## Liveness and readiness

The distinction matters more than it looks. A failing Kubernetes **liveness**
probe restarts the pod, so a dependency check there turns a brief MongoDB blip
into a restart loop across every instance at once — an outage where there was
only degradation. A failing **readiness** probe removes the instance from the
load balancer and leaves it running to recover.

```mermaid
sequenceDiagram
    autonumber
    participant K as Kubernetes or a load balancer
    participant A as appserver
    participant DB as MongoDB
    participant Rd as Redis

    rect rgb(240, 255, 240)
        K->>A: GET /healthz
        A-->>K: 200 always, while the process runs
        Note over A: Checks NOTHING. Never add a dependency<br/>check here — a database blip would become<br/>a cluster-wide restart loop.
    end

    rect rgb(240, 248, 255)
        K->>A: GET /readyz
        A->>A: 2 second timeout over every registered check
        par all registered checks
            A->>DB: Ping
        and only where a domain registered it
            A->>Rd: Ping
        end
        alt any check fails
            A-->>K: 503 with the failing dependency named
            Note over K,A: Removed from the load balancer.<br/>Still running, so it rejoins by itself<br/>when the dependency returns.
        else all pass
            A-->>K: 200 ready
        end
    end
```

## Startup

Fail fast, and report **every** problem at once — a misconfigured deployment
should not have to be fixed one variable per restart.

```mermaid
sequenceDiagram
    autonumber
    participant M as cmd/<service>/main
    participant Cfg as config
    participant Tr as tracing
    participant DB as MongoDB
    participant K as Kafka
    participant A as appserver

    M->>Cfg: Load()
    Cfg->>Cfg: collect every problem, not just the first
    alt anything is missing or invalid
        Cfg-->>M: one error listing all of them
        M->>M: print to stderr and exit 1
        Note over M,Cfg: No default for the JWT secret or the<br/>Mongo URI. A service that starts with a<br/>guessed security parameter is worse than<br/>one that refuses to start.
    else valid
        M->>Tr: Init — install the propagator either way
        Note over Tr: With no collector, a no-op provider. But the<br/>propagator is still installed, so an inbound<br/>trace context is passed on rather than dropped.
        M->>DB: connect and ping
        Note over M,DB: mongo.Connect never contacts the server,<br/>so the ping is what turns an unreachable<br/>database into a startup failure.
        M->>DB: EnsureIndexes, owned by each domain's adapter
        opt this service uses Kafka
            M->>K: EnsureTopic for every topic it publishes or consumes
        end
        M->>A: register ready checks, background tasks, shutdown hooks
        M->>A: Run — serve until a signal
    end
```

## Graceful shutdown

The ordering is deliberate: the API drains first so the last requests are still
measurable while the metrics endpoint is up to be scraped one final time, and
background tasks finish before their clients are disconnected underneath them.

```mermaid
sequenceDiagram
    autonumber
    participant OS as SIGINT or SIGTERM
    participant A as appserver
    participant API as API server
    participant G as gRPC server
    participant Adm as admin server
    participant T as background tasks
    participant Cl as closers

    OS->>A: signal
    A->>A: cancel the root context
    A->>API: Shutdown with SHUTDOWN_TIMEOUT
    Note over A,API: API first, so the final requests are still<br/>measurable while /metrics can be scraped once more.
    API-->>A: in-flight requests drained
    A->>G: GracefulStop
    G-->>A: open RPCs completed
    A->>Adm: Shutdown
    A->>T: contexts already cancelled — relay, consumers, reaper, count logger
    T-->>A: every loop returned
    Note over A,T: Tasks finish BEFORE the closers run, so<br/>nothing is mid-query when its client is<br/>disconnected underneath it.
    loop closers, in reverse order of registration
        A->>Cl: close Kafka, close Redis, flush traces, disconnect Mongo
    end
    A-->>OS: exit 0
```
