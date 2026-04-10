FROM golang:1.25-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
        libgpgme-dev \
        libassuan-dev \
        libbtrfs-dev \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /ghenkins ./cmd/ghenkins/

# ── runtime ───────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        libgpgme11 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /ghenkins /usr/local/bin/ghenkins

ENTRYPOINT ["ghenkins"]
CMD ["serve"]
