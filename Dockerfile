# Build stage.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies are copied first so the module download layer is reused
# whenever only source files change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is off so the binary runs on the distroless static image below.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# Runtime stage: no shell, no package manager, non-root by default.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/api /api

USER nonroot:nonroot
EXPOSE 8080 9090

ENTRYPOINT ["/api"]
