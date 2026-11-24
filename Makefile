BINARY=gomcrouter
IMAGE=gomcrouter
PKG=github.com/tentens-tech/gomcrouter
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_COMMIT := $(shell git rev-list -1 HEAD)
LDFLAGS=-X '$(PKG)/version.Version=$(VERSION)'

.DEFAULT_GOAL := build
.PHONY: help version lint vuln sec test test-race cover e2e clean build docker-build release-tag

help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

version: ## Show version
	@echo $(VERSION) \(git commit: $(GIT_COMMIT)\)

lint: ## Run linters
	golangci-lint run --color always ./...

vuln: ## Check dependencies and stdlib for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

sec: ## Run gosec static security analysis
	go run github.com/securego/gosec/v2/cmd/gosec@latest ./...

test: ## Run only quick tests
	go test -short ./...

test-race: ## Run all unit tests with the race detector
	go test -race ./...

cover: ## Run unit tests with coverage, writes coverage.out
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

e2e: ## Run the docker-compose example and its end-to-end test
	cd examples/docker && docker compose up --build --detach --wait && \
	./test.sh; rc=$$?; docker compose down --volumes; exit $$rc

build: ## Build the binary with the version injected
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

docker-build: ## Build the docker image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) . && \
	echo "docker run --rm $(IMAGE):$(VERSION) /bin/$(BINARY) --version"

release-tag: ## Create and push a release tag, e.g. make release-tag TAG=v1.4.0
	@test -n "$(TAG)" || { echo "usage: make release-tag TAG=vX.Y.Z"; exit 1; }
	@echo "$(TAG)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$' || { echo "TAG must look like vX.Y.Z"; exit 1; }
	git tag -a $(TAG) -m "Release $(TAG)"
	git push origin refs/tags/$(TAG)

clean: ## Remove build outputs
	rm -f $(BINARY) coverage.out junit.xml
