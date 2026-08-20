# One Dockerfile for every service: the build differs only in which main
# package is compiled, so a per-service copy would be four identical files that
# drift apart.
#
#   docker build --build-arg SERVICE=product -t product .
ARG SERVICE=user

FROM golang:1.26-alpine AS build
ARG SERVICE

WORKDIR /src

# Dependencies first, so the module download layer is reused whenever only
# source files change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is off so the binary runs on the distroless static image below.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${SERVICE}

# An empty directory, carried into the runtime image so it exists there owned by
# the non-root user. Docker seeds a fresh named volume from the image path it is
# mounted over, ownership included — without this, /certs would be created by
# the daemon owned by root, certgen could not write into it, and the 0600
# private keys it writes could not be read back by the services.
RUN mkdir -p /out/certs

# Runtime stage: no shell, no package manager, non-root by default.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/service /service
COPY --from=build --chown=65532:65532 /out/certs /certs

USER nonroot:nonroot
EXPOSE 8080 9090 6060

ENTRYPOINT ["/service"]
