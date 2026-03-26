FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.* ./
RUN go mod download
COPY . .
RUN go install github.com/pressly/goose/v3/cmd/goose@v3.24.3
RUN CGO_ENABLED=0 go build -o api ./cmd/api
RUN CGO_ENABLED=0 go build -o worker ./cmd/worker

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/api .
COPY --from=builder /app/worker .
COPY --from=builder /go/bin/goose /usr/local/bin/goose
COPY db/migrations db/migrations
COPY scripts/docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

ENV GIN_MODE=release
ENV RUN_MIGRATIONS=true

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["./api"]
