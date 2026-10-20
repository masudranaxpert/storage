# Multi-stage build for Stream Mesh (Coordinator & Worker Agent)
ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/stream ./cmd/stream

FROM debian:bookworm-slim

# Install CA certificates for HTTPS/S3 and FFmpeg for worker remuxing
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    ffmpeg \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binary to system path and cached agent binary for SSH provisioning
COPY --from=builder /out/stream /usr/local/bin/stream
COPY --from=builder /out/stream /app/stream-linux-amd64
COPY web /app/web

# Create persistent storage directories
RUN mkdir -p /app/data /stream/media /stream/processing

# 1212: Web Dashboard & REST API
# 9090: gRPC Mesh Control Plane
# 2052: Worker Agent HTTP & Byte-Range Streaming
EXPOSE 1212 9090 2052

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:1212/api/health || curl -f http://localhost:2052/status || exit 1

ENTRYPOINT ["stream"]
CMD ["coordinator", "--port=1212", "--grpc-port=9090"]
