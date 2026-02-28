# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o ParentalParrot .

# Run stage
FROM alpine:3.20
RUN apk --no-cache add ca-certificates && mkdir -p /data
WORKDIR /app

COPY --from=builder /build/ParentalParrot .
COPY --from=builder /build/config.example.yaml .

EXPOSE 8080

# Default: DB and state in /data; seed from bundled example when DB is empty
ENTRYPOINT ["/app/ParentalParrot"]
CMD ["-db", "/data/parentalparrot.db", "-seed", "/app/config.example.yaml", "-listen", ":8080"]
