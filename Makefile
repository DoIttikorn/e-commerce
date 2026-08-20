# Default service for the targets that act on one binary.
SERVICE ?= user

.DEFAULT_GOAL := help

## help: list the targets
help:
	@echo "Usage: make <target> [SERVICE=user|seller|product]"
	@echo
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## all: build then run the unit tests
all: build test

## build: compile every service into ./bin
build:
	@echo "Building..."
	@go build -o bin/ ./cmd/...

## run: run one service from source — make run SERVICE=product
run:
	@go run ./cmd/$(SERVICE)

## watch: live reload one service — make watch SERVICE=seller
watch:
	@command -v air >/dev/null || { \
		echo "air is not installed. Run: go install github.com/air-verse/air@latest"; \
		exit 1; \
	}
	@air --build.cmd "go build -o ./tmp/main ./cmd/$(SERVICE)"

## lint: gofmt and vet, exactly what CI runs
lint:
	@test -z "$$(gofmt -l . )" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	@go vet ./...

## test: unit tests only — no infrastructure needed
test:
	@echo "Testing..."
	@go test -short -race -v ./...

## itest: every test, including integration — needs MongoDB, Redis and Kafka
itest:
	@echo "Running the full suite..."
	@go test -race -timeout 10m -v ./...

## load-smoke: k6 sanity check — 1 user, proves the stack is wired
load-smoke:
	@k6 run test/load/smoke.js

## load-read: k6 load on the cached catalogue read
load-read:
	@k6 run test/load/product_read.js

## load-auth: k6 load on login, which is bcrypt-bound on purpose
load-auth:
	@k6 run test/load/auth.js

## proto: regenerate gRPC code from the .proto contracts
proto:
	@echo "Generating protobuf..."
	@protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		api/user/v1/*.proto

## docker-run: build and start everything — three services, MongoDB, Redis, Kafka
##             --remove-orphans clears containers left by an earlier compose file
docker-run:
	@docker compose up --build --remove-orphans

## docker-logs: follow the logs — make docker-logs SERVICE=product
docker-logs:
	@docker compose logs -f $(SERVICE)

## docker-down: stop the stack, keeping the data volumes
docker-down:
	@docker compose down

## docker-clean: stop the stack and delete the data volumes as well
docker-clean:
	@docker compose down --volumes --remove-orphans

## clean: remove build output
clean:
	@echo "Cleaning..."
	@rm -rf bin tmp

.PHONY: help all build run watch lint test itest load-smoke load-read load-auth proto docker-run docker-logs docker-down docker-clean clean
