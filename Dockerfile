# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod ./
RUN go mod download || true

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/ironclavebot ./cmd/ironclavebot

# Final lightweight runtime image
FROM alpine:3.20

RUN apk add --no-cache ca-certificates git bash curl

WORKDIR /ironclave
COPY --from=builder /app/ironclavebot /usr/local/bin/ironclavebot

EXPOSE 8080

VOLUME ["/ironclave/data", "/ironclave/workspace"]

ENTRYPOINT ["ironclavebot"]
CMD ["-port", "8080", "-workdir", "/ironclave/workspace"]
