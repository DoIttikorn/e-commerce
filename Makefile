BINARY := main

all: build test

## build: compile the api binary
build:
	@echo "Building..."
	@go build -o $(BINARY) ./cmd/api

## run: run the api from source
run:
	@go run ./cmd/api

## test: unit tests only, no database required
test:
	@echo "Testing..."
	@go test -short -race -v ./...

## itest: full suite including integration tests, needs MongoDB running
itest:
	@echo "Running integration tests..."
	@go test -race -v ./...

## proto: regenerate gRPC code from the .proto contracts
proto:
	@echo "Generating protobuf..."
	@protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		api/user/v1/*.proto

## lint: formatting and vet checks, same as CI
lint:
	@test -z "$$(gofmt -l . )" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	@go vet ./...

## docker-run: start the api and MongoDB
docker-run:
	@docker compose up --build

## docker-down: stop the stack
docker-down:
	@docker compose down

## watch: live reload via air
watch:
	@if command -v air > /dev/null; then \
		air; \
	else \
		echo "air is not installed. Run: go install github.com/air-verse/air@latest"; \
		exit 1; \
	fi

## clean: remove build output
clean:
	@echo "Cleaning..."
	@rm -f $(BINARY)

.PHONY: all build run test itest proto lint docker-run docker-down watch clean
