# Coordinator image only — no ffmpeg/aria2 (workers get those on the VPS via add-vps).
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/stream ./cmd/stream

FROM debian:bookworm-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /out/stream /usr/local/bin/stream
COPY web /app/web

EXPOSE 8080 9090
ENTRYPOINT ["stream"]
