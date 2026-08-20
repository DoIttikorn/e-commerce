# gRPC request collection

The gRPC surface is deliberately smaller than the REST one: `CreateUser` and
`GetUser` only. Registration and login are not here, and will not be — a service
must never authenticate *as* a user, it propagates the user's token. See
[../../docs/tech-stack.md](../../docs/tech-stack.md) for why REST and gRPC face
different callers rather than competing.

Examples use [grpcurl](https://github.com/fullstorydev/grpcurl). The server
registers gRPC reflection, so no local copy of the `.proto` is needed:

```bash
brew install grpcurl
```

## Discover the surface

```bash
grpcurl -plaintext localhost:9090 list
grpcurl -plaintext localhost:9090 describe user.v1.UserService
```

## Get a token

Credentials are exchanged over REST; gRPC only ever sees the resulting token.

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"ittikorn@example.com","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')
```

## CreateUser

The token travels in **metadata**, not in an HTTP header — the one part of
authentication that differs between the two adapters.

```bash
grpcurl -plaintext \
  -H "authorization: Bearer $TOKEN" \
  -d '{"name":"Somchai","email":"somchai@example.com","password":"another-long-enough-password"}' \
  localhost:9090 user.v1.UserService/CreateUser
```

```json
{
  "user": {
    "id": "665f1c0e5b3a4c001f2d9a11",
    "name": "Somchai",
    "email": "somchai@example.com",
    "createdAt": "2026-08-20T09:00:00Z"
  }
}
```

The `User` message has no password field, so a hash cannot be serialised into
it even by mistake.

## GetUser

This is the call the other domains will make: Order, Seller and Product all need
to turn a stored user ID into a name.

```bash
grpcurl -plaintext \
  -H "authorization: Bearer $TOKEN" \
  -d '{"id":"665f1c0e5b3a4c001f2d9a11"}' \
  localhost:9090 user.v1.UserService/GetUser
```

## Failure cases

The same domain errors the REST adapter renders as status codes arrive here as
gRPC codes. One set of rules, two vocabularies.

| Situation | REST | gRPC |
|---|---|---|
| no or bad token | 401 | `Unauthenticated` |
| unknown ID | 404 | `NotFound` |
| duplicate email | 409 | `AlreadyExists` |
| malformed ID | 400 | `InvalidArgument` |
| invalid input | 400 + `fields` | `InvalidArgument` + `BadRequest` details |

```bash
# No token at all -> Unauthenticated
grpcurl -plaintext -d '{"id":"anything"}' \
  localhost:9090 user.v1.UserService/GetUser

# Malformed ID -> InvalidArgument
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"id":"not-an-object-id"}' \
  localhost:9090 user.v1.UserService/GetUser

# Well-formed but absent -> NotFound
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"id":"000000000000000000000000"}' \
  localhost:9090 user.v1.UserService/GetUser

# Invalid input -> InvalidArgument carrying per-field violations, the
# counterpart of the REST "fields" object.
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"name":"","email":"not-an-email","password":"x"}' \
  localhost:9090 user.v1.UserService/CreateUser
```

## Regenerating

`api/user/v1/user.pb.go` and `user_grpc.pb.go` are generated and committed.
After editing the `.proto`:

```bash
make proto
```
