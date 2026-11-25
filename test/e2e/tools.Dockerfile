# The gomcrouter image with tools used by the e2e tests: curl and e2e-client
# (client/). The user stays non-root.
ARG IMAGE=gomcrouter:e2e

FROM golang:1.27-alpine AS client
WORKDIR /src
COPY client/main.go .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /e2e-client main.go

FROM ${IMAGE}
USER 0
RUN apk add --no-cache curl
COPY --from=client /e2e-client /usr/local/bin/e2e-client
USER 10001:10001
