# Stage 1: Build the Go binary
FROM golang:1.25.4-bookworm AS builder

WORKDIR /app

# Copy go.mod and go.sum first for better cache usage
COPY go.mod go.sum ./
RUN go mod download

# Then copy the rest of the source
COPY . .

# Build with static linking (native to the builder image's platform)
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o lb2120-agent .

# Stage 2: Create minimal runtime image
FROM debian:bookworm-slim

# Fixed UID/GID so the bind-mounted state volume's ownership on the host is
# predictable across rebuilds.
RUN groupadd -r -g 1500 lb2120agent && useradd -r -u 1500 -g lb2120agent lb2120agent

# Copy binary from builder
COPY --from=builder /app/lb2120-agent /usr/local/bin/lb2120-agent

USER lb2120agent

ENTRYPOINT ["/usr/local/bin/lb2120-agent"]
