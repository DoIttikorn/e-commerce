# User — sequence diagrams

*ภาษาไทย: [user.th.md](user.th.md) · Index: [README.md](README.md)*

The first domain, and the template every other one copies. Two driving adapters
— REST and gRPC — over one service, with no branching inside it.

| Flow | Endpoint |
|---|---|
| [Register](#register) | `POST /api/v1/auth/register` |
| [Login](#login) | `POST /api/v1/auth/login` |
| [Bearer authentication](#bearer-authentication-every-protected-route) | every protected route |
| [List users](#list-users) | `GET /api/v1/users` |
| [Get one user](#get-one-user) | `GET /api/v1/users/{id}` |
| [Create a user](#create-a-user) | `POST /api/v1/users` |
| [Update](#update-patch-not-put) | `PATCH /api/v1/users/{id}` |
| [Delete](#delete) | `DELETE /api/v1/users/{id}` |
| [gRPC](#grpc-createuser-and-getuser) | `user.v1.UserService/*` |

---

## Register

The interesting part is the failure branch. Uniqueness is enforced by a **unique
index in MongoDB**, not by an application-level "does this email exist?" query
before the insert — that check races, and two concurrent registrations would
both pass it and both insert.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: POST /api/v1/auth/register
    H->>H: decode body and validate
    alt invalid input
        H-->>C: 400 validation failed plus fields
    else valid
        H->>S: Register(ctx, NewUser)
        S->>S: bcrypt hash of the password
        S->>R: Create(ctx, User)
        R->>DB: insertOne on users
        alt duplicate key E11000
            DB-->>R: write error
            R-->>S: ErrEmailTaken
            S-->>H: ErrEmailTaken
            H-->>C: 409 email already registered
        else inserted
            DB-->>R: inserted id
            R-->>S: User
            S-->>H: User
            H-->>C: 201 user, never the password hash
        end
    end
```

## Login

An unknown email and a wrong password take different internal paths and produce
the **same** 401, so login cannot be used to discover which addresses are
registered. The bcrypt comparison is deliberately slow — it is the reason the
login endpoint benchmarks at 63 req/s against the catalogue's 13,818.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant T as auth issuer

    C->>H: POST /api/v1/auth/login
    H->>S: Login(ctx, email, password)
    S->>R: ByEmail(ctx, email)
    alt no such user
        R-->>S: ErrUserNotFound
        S-->>H: ErrInvalidCredentials
    else found
        R-->>S: User with password hash
        S->>S: bcrypt compare
        alt hash does not match
            S-->>H: ErrInvalidCredentials
        else matches
            S->>T: Issue(subject = user id)
            T-->>S: signed token and expiry
            S-->>H: token
        end
    end
    alt credentials rejected
        H-->>C: 401 invalid credentials, identical either way
    else accepted
        H-->>C: 200 token and expires_at
    end
```

## Bearer authentication (every protected route)

Verification pins the signing algorithm **before** trusting any claim. Without
that, a token declaring `none` verifies with no signature at all, and one
declaring `RS256` can be signed using the public key as an HMAC secret.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant MW as auth middleware
    participant V as auth verifier
    participant H as handler

    C->>MW: request with Authorization Bearer token
    alt header missing or not Bearer
        MW-->>C: 401 unauthorized
    else present
        MW->>V: Verify(token)
        V->>V: check the signing method matches first
        alt wrong algorithm, bad signature, or expired
            V-->>MW: error
            MW-->>C: 401 unauthorized
        else valid
            V-->>MW: claims with subject
            MW->>MW: put the subject on the request context
            MW->>H: next.ServeHTTP
            H-->>C: the handler's own response
        end
    end
```

## List users

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/users?limit=20&offset=0
    H->>H: clamp limit and offset to sane bounds
    H->>S: List(ctx, limit, offset)
    S->>R: List(ctx, limit, offset)
    par one round trip each
        R->>DB: find with skip and limit
        R->>DB: countDocuments
    end
    DB-->>R: page and total
    R-->>S: users and total
    S-->>H: users and total
    H-->>C: 200 users, total, limit, offset
```

## Get one user

A **malformed** ID is 400 and a well-formed one that names nothing is 404. They
are different mistakes, and collapsing them loses information the caller needs.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>H: GET /api/v1/users/{id}
    H->>S: ByID(ctx, id)
    S->>R: ByID(ctx, id)
    alt not a MongoDB ObjectID
        R-->>S: ErrInvalidID
        S-->>H: ErrInvalidID
        H-->>C: 400 malformed user id
    else well formed
        R->>DB: findOne by _id
        alt no document
            DB-->>R: ErrNoDocuments
            R-->>S: ErrUserNotFound
            S-->>H: ErrUserNotFound
            H-->>C: 404 user not found
        else found
            DB-->>R: document
            R-->>S: User
            S-->>H: User
            H-->>C: 200 user
        end
    end
```

## Create a user

Same write as Register, different authorization: this one requires a token.
"Sign me up" and "add a user to the system" are different operations even though
they produce the same row.

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant MW as auth middleware
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter

    C->>MW: POST /api/v1/users with bearer token
    MW->>H: authenticated request
    H->>H: validate the body
    H->>S: Create(ctx, NewUser)
    S->>S: bcrypt hash
    S->>R: Create(ctx, User)
    R-->>S: User or ErrEmailTaken
    S-->>H: result
    H-->>C: 201 user, or 409 on a duplicate email
```

## Update (PATCH, not PUT)

The brief asks to update a name **or** an email, so an omitted field must be left
alone. A struct of plain strings cannot express that — an absent field and an
empty one look identical — so the request type uses `*string`: `nil` means "not
supplied", `""` means "set it to empty".

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant MW as auth middleware
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter
    participant DB as MongoDB

    C->>MW: PATCH /api/v1/users/{id}
    MW->>H: subject on the context
    H->>H: requireSelf, compare subject to path id
    alt a different user
        H-->>C: 403 forbidden
    else own record
        H->>H: decode into pointer fields
        alt no field supplied at all
            H-->>C: 400 at least one field is required
        else at least one
            H->>S: Update(ctx, id, Update)
            S->>R: Update(ctx, id, Update)
            R->>DB: updateOne with only the supplied fields in $set
            alt email now collides
                DB-->>R: duplicate key
                R-->>S: ErrEmailTaken
                H-->>C: 409 email already registered
            else updated
                DB-->>R: updated document
                R-->>S: User
                S-->>H: User
                H-->>C: 200 user
            end
        end
    end
```

## Delete

```mermaid
sequenceDiagram
    autonumber
    actor C as Client
    participant MW as auth middleware
    participant H as handler
    participant S as user.Service
    participant R as mongodb adapter

    C->>MW: DELETE /api/v1/users/{id}
    MW->>H: subject on the context
    H->>H: requireSelf
    alt a different user
        H-->>C: 403 forbidden
    else own record
        H->>S: Delete(ctx, id)
        S->>R: Delete(ctx, id)
        R-->>S: ok or ErrUserNotFound
        S-->>H: result
        H-->>C: 204 no content, or 404
    end
    Note over C,H: The token stays valid until it expires.<br/>A stateless token cannot be revoked<br/>without adding back the session lookup<br/>it exists to avoid.
```

## gRPC: CreateUser and GetUser

Two adapters over one service is the point of the hexagon: `gapi` and `handler`
call the identical `user.Service`, and the service knows about neither HTTP
status codes nor gRPC codes. **Each adapter owns its own error mapping.**

The gRPC surface is deliberately only these two. `ListUsers` and `Login` are
anti-patterns there: a service must never authenticate *as* a user, it
propagates the user's token.

```mermaid
sequenceDiagram
    autonumber
    participant Svc as calling service
    participant I as gapi interceptor
    participant G as gapi server
    participant S as user.Service
    participant R as mongodb adapter

    Svc->>I: CreateUser or GetUser with token in metadata
    Note over I: The token comes from gRPC metadata,<br/>never an Authorization header.
    alt metadata missing or token invalid
        I-->>Svc: Unauthenticated
    else valid
        I->>G: handler with the subject on the context
        G->>G: validate the request message
        alt invalid
            G-->>Svc: InvalidArgument with errdetails.BadRequest violations
        else valid
            G->>S: the same method the REST handler calls
            S->>R: repository call
            R-->>S: entity or sentinel error
            S-->>G: entity or sentinel error
            alt ErrEmailTaken
                G-->>Svc: AlreadyExists
            else ErrUserNotFound
                G-->>Svc: NotFound
            else ErrInvalidID
                G-->>Svc: InvalidArgument
            else ok
                G-->>Svc: user message, no password field
            end
        end
    end
```
