ARG IMAGE=alpine
ARG TAG=latest
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /build
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -ldflags "-X 'github.com/tentens-tech/gomcrouter/version.Version=${VERSION}'" -o gomcrouter .

FROM ${IMAGE}:${TAG}
COPY --from=build /build/gomcrouter /bin/gomcrouter

RUN addgroup -S gomcrouter -g 10001 \
 && adduser -S gomcrouter -G gomcrouter -u 10001

USER 10001:10001
CMD ["/bin/gomcrouter"]
