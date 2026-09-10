# Stage 1: Build
FROM golang:1.27.1-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends nodejs npm python3 git make ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN make build

# Stage 2: Runtime
FROM alpine:3.19

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/bin/gateway /gateway
COPY --from=builder /app/bin/picoclaw /usr/local/lib/svkexe/picoclaw
COPY agent/licenses /usr/local/share/licenses/svkexe-agent

EXPOSE 8080

CMD ["/gateway"]
