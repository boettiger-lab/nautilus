# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
# CGO_ENABLED=0: modernc.org/sqlite is a pure-Go SQLite port — no C compiler needed.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o s3gw .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.20

# ca-certificates so TLS connections to Ceph S3 work.
RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/s3gw .

# Default data directory for the SQLite database.
RUN mkdir -p /data

EXPOSE 8080
ENTRYPOINT ["/app/s3gw"]
