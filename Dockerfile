# The builder runs on the build host and cross-compiles for the target
# platform, so a multi-arch build doesn't need emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS=linux
ARG TARGETARCH
# Version reported by `gomcrouter --version`.
ARG VERSION=dev

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags "-s -w -X 'github.com/tentens-tech/gomcrouter/version.Version=${VERSION}'" -o gomcrouter .

FROM alpine:3.24

ARG VERSION=dev
LABEL org.opencontainers.image.title="gomcrouter" \
      org.opencontainers.image.description="High-performance memcached router" \
      org.opencontainers.image.source="https://github.com/tentens-tech/gomcrouter" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"

# Unprivileged runtime user. Numeric UID/GID so that Kubernetes
# `runAsNonRoot: true` can verify it without resolving names.
RUN addgroup -S -g 10001 gomcrouter && \
    adduser -S -D -H -u 10001 -G gomcrouter -h /nonexistent -s /sbin/nologin gomcrouter

# The binary is owned by root and read-only for everyone, so the runtime user
# can't modify it. gomcrouter doesn't write to disk, so the image works with a
# read-only root filesystem.
COPY --from=build --chown=0:0 --chmod=0555 /build/gomcrouter /bin/gomcrouter

USER 10001:10001
CMD ["/bin/gomcrouter"]
