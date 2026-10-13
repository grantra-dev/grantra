.PHONY: build test test-db lint up down

build:
	cd core && go build -o ../bin/grantra ./cmd/grantra

test:
	cd core && go test -race ./...

# Runs the database tests too; needs a disposable Postgres, e.g.
#   make test-db GRANTRA_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
test-db:
	cd core && GRANTRA_TEST_DATABASE_URL=$(GRANTRA_TEST_DATABASE_URL) go test -race -count=1 ./...

lint:
	cd core && go vet ./... && test -z "$$(gofmt -l .)"

up:
	docker compose -f deploy/docker-compose.yml up --build

down:
	docker compose -f deploy/docker-compose.yml down
