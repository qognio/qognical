# syntax=docker/dockerfile:1.7
#
# Multi-stage build for qognical. Stage 1 builds a static binary against
# pure-Go SQLite (modernc/sqlite — no CGO). Stage 2 is a tiny Alpine image
# carrying the binary, tzdata for IANA timezone support, and ca-certificates
# for outbound HTTPS to provider APIs.

ARG GO_VERSION=1.25
ARG ALPINE_VERSION=3.20

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Pre-cache dependencies. The cache mounts are picked up by BuildKit when
# available; without BuildKit the steps still run, just without layer cache.
COPY go.mod go.sum ./
RUN go mod download

# Build.
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" \
        -o /out/qognical ./cmd/qognical

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S qognical && adduser -S -G qognical qognical && \
    mkdir -p /pb_data && chown qognical:qognical /pb_data
COPY --from=build /out/qognical /qognical

USER qognical
EXPOSE 8090
VOLUME /pb_data

# CLI healthcheck — no curl needed, the binary does it.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/qognical", "healthcheck", "--dir=/pb_data"]

ENTRYPOINT ["/qognical"]
CMD ["serve", "--http=0.0.0.0:8090", "--dir=/pb_data"]
