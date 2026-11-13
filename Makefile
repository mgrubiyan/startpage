TEST_DB_CONTAINER ?= startpage-testdb
TEST_DB_PORT      ?= 55432
TEST_DATABASE_URL ?= postgres://postgres:test@127.0.0.1:$(TEST_DB_PORT)/postgres?sslmode=disable
DEMO_DATABASE_URL ?= postgres://postgres:test@127.0.0.1:$(TEST_DB_PORT)/startpage_demo?sslmode=disable

.PHONY: build run run-demo test test-db test-integration e2e lint up down

build:
	CGO_ENABLED=0 go build -trimpath -o bin/startpage ./cmd/startpage

## run: start locally against the test database (make test-db first)
run:
	DATABASE_URL='$(TEST_DATABASE_URL)' STARTPAGE_PASSWORD=$${STARTPAGE_PASSWORD:-correct-horse} go run ./cmd/startpage

## run-demo: read-only demo mode, in its own database so the seed stays out of `make run` data
run-demo:
	docker exec $(TEST_DB_CONTAINER) psql -U postgres -tAc "SELECT 1 FROM pg_database WHERE datname = 'startpage_demo'" | grep -q 1 \
		|| docker exec $(TEST_DB_CONTAINER) createdb -U postgres startpage_demo
	DATABASE_URL='$(DEMO_DATABASE_URL)' STARTPAGE_DEMO=true go run ./cmd/startpage

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

## e2e: the page in a real browser (make test-db first; installs Playwright's Chromium once)
e2e:
	docker exec $(TEST_DB_CONTAINER) psql -U postgres -tAc "SELECT 1 FROM pg_database WHERE datname = 'startpage_e2e'" | grep -q 1 \
		|| docker exec $(TEST_DB_CONTAINER) createdb -U postgres startpage_e2e
	cd e2e && npm ci && npx playwright install chromium && npx playwright test

lint:
	go vet ./...
	golangci-lint run

up:
	docker compose up -d --build

down:
	docker compose down
