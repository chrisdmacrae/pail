# Pail's everyday commands. `make` on its own lists them.

UI := web/ui
DOCS := docs
ASTRO := adapters/astro
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
.PHONY: help setup build ui install test test-go test-ui test-astro lint lint-go lint-ui lint-astro fmt check \
        dev dev-storage dev-server dev-ui dev-docs docs clean \
        kvm-up kvm-check kvm-smoke kvm-shell kvm-down dev-kvm test-kvm release

help: ## List these commands
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## Setting up

setup: ## Install what development needs: UI and docs packages, and a local versitygw
	cd $(UI) && pnpm install
	cd $(ASTRO) && pnpm install
	cd $(DOCS) && pnpm install
	go install github.com/versity/versitygw/cmd/versitygw@latest

## Building

build: ui ## Build bin/pail-server (with the web UI inside) and bin/pail
	go build -o $(BIN)/pail-server ./cmd/pail-server
	go build -o $(BIN)/pail ./cmd/pail

ui: ## Build the web UI into the folder pail-server embeds
	cd $(UI) && pnpm install --frozen-lockfile && pnpm build

docs: ## Build the documentation site into docs/dist
	cd $(DOCS) && pnpm install --frozen-lockfile && pnpm build

release: ui ## Build what a release publishes into dist/release (VERSION=v0.1.0)
	scripts/release $(or $(VERSION),dev)

install: ## Install the pail command into your Go bin
	go install ./cmd/pail

## Checking

test: test-go test-ui test-astro ## Run every test

test-go: ## Run the Go tests with the race detector
	go test -race ./...

test-ui: ## Run the web UI's tests
	cd $(UI) && pnpm test

test-astro: ## Run the Astro adapter's tests
	cd $(ASTRO) && pnpm test

lint: lint-go lint-ui lint-astro ## Lint everything; changes nothing

lint-go: ## go vet, and fail on files gofmt would change
	go vet ./...
	@unformatted="$$(gofmt -l $(GOFILES))"; \
	if [ -n "$$unformatted" ]; then echo "gofmt would change:"; echo "$$unformatted"; exit 1; fi

lint-ui: ## Type-check the web UI and run Biome over it
	cd $(UI) && pnpm typecheck && pnpm lint

lint-astro: ## Run Biome over the Astro adapter
	cd $(ASTRO) && pnpm lint

fmt: ## Format Go, the web UI and the Astro adapter in place
	gofmt -w $(GOFILES)
	cd $(UI) && pnpm format
	cd $(ASTRO) && pnpm format

check: lint test docs ## What to run before a commit: lint, test, and build the docs

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

dev-docs: ## Run the documentation site with hot reload on :4321
	cd $(DOCS) && pnpm dev

## The KVM host: where Firecracker runs
#
# Firecracker needs Linux with KVM. KVM_HOST says where that is: "lima" (the
# default: a Lima VM on this machine, for Apple M3 or later) or user@host for
# any Linux machine over SSH. For example: make kvm-up KVM_HOST=chris@homelab
export KVM_HOST ?= lima

kvm-up: ## Make the KVM host ready: start the Lima VM (or check the SSH host), install Firecracker
	scripts/kvm-host up

kvm-check: ## Say whether the KVM host can run microVMs
	scripts/kvm-host check

kvm-smoke: ## Boot one microVM on the KVM host, to see that it can
	scripts/kvm-host smoke

kvm-shell: ## Open a shell on the KVM host
	scripts/kvm-host shell

kvm-down: ## Stop the Lima VM
	scripts/kvm-host down

dev-kvm: ui ## Run Pail on the KVM host, with microVMs, on :8080
	scripts/kvm-host run

test-kvm: ## Run the tests that boot real microVMs, on the KVM host
	scripts/kvm-host test

clean: ## Remove what the build made
	rm -rf $(BIN) dist $(DOCS)/dist $(DOCS)/.astro
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
