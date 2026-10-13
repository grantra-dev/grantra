# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f core/Dockerfile .
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY core/go.mod core/go.sum ./
RUN go mod download
COPY core/ ./
COPY --from=web /web/dist/ ./internal/webui/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/grantra-dev/grantra/core/internal/server.Version=${VERSION}" \
    -o /out/grantra ./cmd/grantra

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/grantra /grantra
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/grantra"]
