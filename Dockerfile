FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder
WORKDIR /app
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o api ./cmd/api
RUN CGO_ENABLED=0 go build -o worker ./cmd/worker
RUN CGO_ENABLED=0 go build -o migrate ./cmd/migrate

FROM alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
WORKDIR /app
RUN addgroup -S catalogo && adduser -S -D -H -u 10001 -G catalogo catalogo
COPY --from=builder /app/api .
COPY --from=builder /app/worker .
COPY --from=builder /app/migrate .
COPY db/migrations db/migrations
COPY scripts/docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

RUN chown -R catalogo:catalogo /app

ENV GIN_MODE=release

USER catalogo
ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["./api"]
