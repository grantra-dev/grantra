.PHONY: build web test test-db lint web-lint web-test up down reset

# Builds the web console and copies it where the Go binary embeds it.
web:
	cd web && npm ci && npm run build
	find core/internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -R web/dist/. core/internal/webui/dist/

build: web
	cd core && go build -o ../bin/grantra ./cmd/grantra

test:
	cd core && go test -race ./...

# Runs the database tests too; needs a disposable Postgres, e.g.
#   make test-db GRANTRA_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
test-db:
	cd core && GRANTRA_TEST_DATABASE_URL=$(GRANTRA_TEST_DATABASE_URL) go test -race -count=1 ./...

lint:
	cd core && go vet ./... && test -z "$$(gofmt -l .)"

web-lint:
	cd web && npm run lint && npm run typecheck

web-test:
	cd web && npm test

up:
	docker compose -f deploy/docker-compose.yml up --build

down:
	docker compose -f deploy/docker-compose.yml down

# Stops the stack and deletes its data, for a fresh demo.
reset:
	docker compose -f deploy/docker-compose.yml down --volumes
