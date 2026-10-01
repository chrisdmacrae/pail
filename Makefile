# Pail's everyday commands. `make` on its own lists them.

UI := web/ui
BIN := bin

# What the dev targets run with. Override any of them: make dev-server PAIL_LISTEN=:9000
PAIL_TOKEN         ?= dev-token
PAIL_BASE_DOMAIN   ?= localhost
PAIL_LISTEN        ?= 127.0.0.1:8080
STORAGE_ADDR       ?= 127.0.0.1:7070
PAIL_S3_ENDPOINT   ?= http://$(STORAGE_ADDR)
PAIL_S3_ACCESS_KEY ?= pail
PAIL_S3_SECRET_KEY ?= pail-dev-secret
DEV_DATA           ?= data
# Both empty: custom hostnames are refused, as on an installation without
# Let's Encrypt. Set both to try hostnames out.
PAIL_ACME_DNS_PROVIDER ?=
PAIL_ACME_DNS_TOKEN    ?=
# Development serves plain HTTP: browsers treat localhost as secure anyway,
# and no wildcard certificate can cover *.localhost. To try HTTPS, use a base
# domain with a dot in it: make dev-server PAIL_TLS=on PAIL_BASE_DOMAIN=pail.test
PAIL_TLS               ?= off
PAIL_LISTEN_TLS        ?= 127.0.0.1:8443

VERSITYGW := $(shell go env GOPATH)/bin/versitygw
GOFILES   := $(shell git ls-files '*.go')

.DEFAULT_GOAL := help
.PHONY: help setup build ui install test test-go test-ui lint lint-go lint-ui fmt check \
        dev dev-storage dev-server dev-ui clean

help: ## List these commands
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## Setting up

setup: ## Install what development needs: UI packages and a local versitygw
	cd $(UI) && pnpm install
	go install github.com/versity/versitygw/cmd/versitygw@latest

## Building

build: ui ## Build bin/pail-server (with the web UI inside) and bin/pail
	go build -o $(BIN)/pail-server ./cmd/pail-server
	go build -o $(BIN)/pail ./cmd/pail

ui: ## Build the web UI into the folder pail-server embeds
	cd $(UI) && pnpm install --frozen-lockfile && pnpm build

install: ## Install the pail command into your Go bin
	go install ./cmd/pail

## Checking

test: test-go test-ui ## Run every test

test-go: ## Run the Go tests with the race detector
	go test -race ./...

test-ui: ## Run the web UI's tests
	cd $(UI) && pnpm test

lint: lint-go lint-ui ## Lint everything; changes nothing

lint-go: ## go vet, and fail on files gofmt would change
	go vet ./...
	@unformatted="$$(gofmt -l $(GOFILES))"; \
	if [ -n "$$unformatted" ]; then echo "gofmt would change:"; echo "$$unformatted"; exit 1; fi

lint-ui: ## Type-check the web UI and run Biome over it
	cd $(UI) && pnpm typecheck && pnpm lint

fmt: ## Format Go and the web UI in place
	gofmt -w $(GOFILES)
	cd $(UI) && pnpm format

check: lint test ## What to run before a commit: lint, then test

## Running locally

# Storage starts first and the server waits for it to answer. Ctrl-C, or any
# of the three stopping, takes the others down with it.
dev: ## Run storage, the server and the UI together; Ctrl-C stops all three
	@test -x $(VERSITYGW) || { echo "versitygw isn't installed. Run make setup."; exit 1; }
	@test -d $(UI)/node_modules || { echo "The UI's packages aren't installed. Run make setup."; exit 1; }
	@trap 'trap - INT TERM EXIT; kill 0' INT TERM EXIT; \
	( $(MAKE) --no-print-directory dev-storage; kill 0 ) & \
	until nc -z $(subst :, ,$(STORAGE_ADDR)) 2>/dev/null; do sleep 0.2; done; \
	( $(MAKE) --no-print-directory dev-server; kill 0 ) & \
	( $(MAKE) --no-print-directory dev-ui; kill 0 ) & \
	wait

dev-storage: ## Run versitygw on :7070, keeping its files in ./data
	@test -x $(VERSITYGW) || { echo "versitygw isn't installed. Run make setup."; exit 1; }
	mkdir -p $(DEV_DATA)
	ROOT_ACCESS_KEY=$(PAIL_S3_ACCESS_KEY) ROOT_SECRET_KEY=$(PAIL_S3_SECRET_KEY) \
		$(VERSITYGW) --port $(STORAGE_ADDR) posix $(DEV_DATA)

dev-server: ui ## Build the web UI, then run pail-server on :8080 against dev-storage
	PAIL_TOKEN=$(PAIL_TOKEN) PAIL_BASE_DOMAIN=$(PAIL_BASE_DOMAIN) PAIL_LISTEN=$(PAIL_LISTEN) \
	PAIL_S3_ENDPOINT=$(PAIL_S3_ENDPOINT) PAIL_S3_ACCESS_KEY=$(PAIL_S3_ACCESS_KEY) PAIL_S3_SECRET_KEY=$(PAIL_S3_SECRET_KEY) \
	PAIL_ACME_DNS_PROVIDER=$(PAIL_ACME_DNS_PROVIDER) PAIL_ACME_DNS_TOKEN=$(PAIL_ACME_DNS_TOKEN) \
	PAIL_TLS=$(PAIL_TLS) PAIL_LISTEN_TLS=$(PAIL_LISTEN_TLS) \
		go run ./cmd/pail-server

dev-ui: ## Run the web UI with hot reload on :5173, using dev-server's API
	cd $(UI) && PAIL_DEV_API=http://$(PAIL_LISTEN) pnpm dev

clean: ## Remove what the build made
	rm -rf $(BIN)
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
