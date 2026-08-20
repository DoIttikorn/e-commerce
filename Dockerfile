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

# Runtime stage: no shell, no package manager, non-root by default.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/service /service

USER nonroot:nonroot
EXPOSE 8080 9090 6060

ENTRYPOINT ["/service"]
