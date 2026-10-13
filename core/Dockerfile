# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/grantra-dev/grantra/core/internal/server.Version=${VERSION}" \
    -o /out/grantra ./cmd/grantra

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/grantra /grantra
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/grantra"]
