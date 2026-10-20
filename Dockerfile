# syntax=docker/dockerfile:1

# Build on the host architecture and cross-compile the static Go binary. This
# avoids running the Go toolchain under QEMU during multi-platform builds.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /bin/pcr-server ./cmd/server

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

# Use a stable numeric identity so container runtimes can enforce non-root execution.
RUN addgroup -S -g 10001 pcr && adduser -S -D -H -u 10001 -G pcr pcr
USER 10001:10001

COPY --from=builder /bin/pcr-server /usr/local/bin/pcr-server

EXPOSE 8080

ENV PCR_ADDR=:8080 \
    PCR_AUTO_MIGRATE=true

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/readyz || exit 1

ENTRYPOINT ["pcr-server"]
