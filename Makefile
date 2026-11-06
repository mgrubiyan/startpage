TEST_DB_CONTAINER ?= startpage-testdb
TEST_DB_PORT      ?= 55432
TEST_DATABASE_URL ?= postgres://postgres:test@127.0.0.1:$(TEST_DB_PORT)/postgres?sslmode=disable

.PHONY: build run test test-db test-integration lint up down

build:
	CGO_ENABLED=0 go build -trimpath -o bin/startpage ./cmd/startpage

## run: start locally against the test database (make test-db first)
run:
	DATABASE_URL='$(TEST_DATABASE_URL)' STARTPAGE_PASSWORD=$${STARTPAGE_PASSWORD:-correct-horse} go run ./cmd/startpage

## test: unit tests only (Postgres tests skip without TEST_DATABASE_URL)
test:
	go test -race ./...

## test-db: throwaway Postgres for integration tests
test-db:
	docker run -d --rm --name $(TEST_DB_CONTAINER) -e POSTGRES_PASSWORD=test \
		-p 127.0.0.1:$(TEST_DB_PORT):5432 postgres:17-alpine
	until docker exec $(TEST_DB_CONTAINER) pg_isready -U postgres >/dev/null 2>&1; do sleep 0.5; done

## test-integration: full suite including Postgres (make test-db first)
test-integration:
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -race -count=1 ./...

lint:
	go vet ./...
	golangci-lint run

up:
	docker compose up -d --build

down:
	docker compose down
