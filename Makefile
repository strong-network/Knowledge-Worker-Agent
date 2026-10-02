BINARY_NAME := knowledge-worker-agent
BUILD_DIR := build
GO_CMD := go
MAIN_PKG := ./cmd/server
RELEASE_IMAGE ?= chat

VERSION := $(shell tr -d ' \t\n\r' < VERSION 2>/dev/null || echo "0.0.0")-$(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
# Customer-facing release tag: the bare VERSION file value, no git SHA.
RELEASE_VERSION := $(shell tr -d ' \t\n\r' < VERSION 2>/dev/null || echo "0.0.0")
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

# npm rewrites node_modules/.package-lock.json on every install, which makes it
# a natural stamp for "the installed tree matches the lockfile". It lives inside
# node_modules deliberately: a stamp kept elsewhere would survive `rm -rf
# node_modules` and leave make convinced the deps were present, failing later in
# `vite build` with a confusing missing-package error.
NODE_MODULES_STAMP := frontend/node_modules/.package-lock.json

.PHONY: all build clean test test-short lint ci run build-and-run deps frontend release help

all: deps frontend test build

## deps: download and tidy Go modules
deps:
	$(GO_CMD) mod tidy
	$(GO_CMD) mod download

## frontend: build the Vue frontend (output to cmd/server/html/)
frontend: $(NODE_MODULES_STAMP)
	cd frontend && npx vite build

# Reinstall only when the lockfile (or manifest) actually changed.
#
# --no-audit is not a security trade-off here: --silent already discards the
# audit report, so the audit POST costs a round trip nobody reads, and audit
# findings never affected `npm ci`'s exit code. Where egress blocks that POST,
# npm waits out its full 300s fetch-timeout, which is minutes of dead build
# time. Dependency scanning belongs in CI, not in a workspace build step.
$(NODE_MODULES_STAMP): frontend/package-lock.json frontend/package.json
	cd frontend && npm ci --silent --no-audit --no-fund
	@touch $@

## build: compile the binary for linux/amd64
build: frontend
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO_CMD) build \
		-ldflags "$(LDFLAGS)" \
		-trimpath \
		-o $(BUILD_DIR)/$(BINARY_NAME) \
		$(MAIN_PKG)
	@echo "Built: $(BUILD_DIR)/$(BINARY_NAME)"

## test: run all tests
test:
	$(GO_CMD) test ./... -v -count=1

## test-short: run tests without verbose output
test-short:
	$(GO_CMD) test ./... -count=1

## lint: run go vet
lint:
	$(GO_CMD) vet ./...

## ci: what CI runs: type-check and build the frontend, vet, build, test, govulncheck
ci:
	cd frontend && npm ci --no-audit --no-fund && npm run build
	$(GO_CMD) vet ./...
	$(GO_CMD) build ./...
	$(GO_CMD) test ./... -count=1
	$(GO_CMD) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

## run: run the existing binary (no build)
run:
	./$(BUILD_DIR)/$(BINARY_NAME)

## build-and-run: build then run locally
build-and-run: build
	./$(BUILD_DIR)/$(BINARY_NAME)

## clean: remove build artifacts
clean:
	rm -rf $(BUILD_DIR)
	$(GO_CMD) clean -cache -testcache

## release: build the release image (Dockerfile.release) as RELEASE_IMAGE:<VERSION>, default chat
release:
	DOCKER_BUILDKIT=1 docker build --build-arg VERSION=$(RELEASE_VERSION) \
		-t $(RELEASE_IMAGE):$(RELEASE_VERSION) -f Dockerfile.release .

## help: show this help
help:
	@echo "Available targets:"
	@grep -E '^## ' Makefile | sed 's/## /  /'
