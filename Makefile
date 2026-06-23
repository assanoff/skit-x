.DEFAULT_GOAL := help

APP         := service-kit-x
GO          ?= go
LINT        ?= golangci-lint
BIN         := bin/$(APP)

# Version is stamped into the binary (cmd.version) and used by `release`.
# Derived from git: the latest tag, or a short SHA (with -dirty) when untagged.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -X github.com/assanoff/service-kit-x/internal/cmd.version=$(VERSION)

# Dev tool versions (override to pin).
GOLANGCI    ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
GOFUMPT     ?= mvdan.cc/gofumpt@latest
GORELEASE   ?= golang.org/x/exp/cmd/gorelease@latest
SWAG        ?= github.com/swaggo/swag/v2/cmd/swag@latest
OASDIFF     ?= github.com/oasdiff/oasdiff@latest
TPARSE      ?= github.com/mfridman/tparse@latest

# Coverage profile written by `make cover` / `make cover-integration`;
# THRESHOLD is the `cover-check` CI gate (measured over the integration suite).
COVERPROFILE ?= coverage.out
THRESHOLD    ?= 65

# OpenAPI spec (REST contract) generated from swag annotations.
SPEC        := docs/swagger.json
SWAG_FLAGS  := -g main.go -o docs --v3.1 --ot json,yaml --parseDependency --parseInternal

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------
.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Develop
# ---------------------------------------------------------------------------
.PHONY: tidy
tidy: ## go mod tidy
	$(GO) mod tidy

.PHONY: fmt
fmt: ## Format code (gofumpt)
	@$(GO) tool gofumpt -w . 2>/dev/null || gofmt -w .

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	$(LINT) run

.PHONY: build
build: ## Build the versioned binary into bin/
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) .
	@echo "built $(BIN) ($(VERSION))"

.PHONY: run
run: ## Run the server (go run . serve)
	$(GO) run -ldflags "$(LDFLAGS)" . serve

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf bin
	rm -f $(COVERPROFILE) coverage.html

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------
.PHONY: test
test: ## Run unit tests (short, race) with per-package coverage
	$(GO) test -race -short -cover ./...

.PHONY: test-json
test-json: ## Unit tests with a pretty pass/fail + coverage summary (tparse)
	@bash -o pipefail -c '$(GO) test -short -race -cover ./... -json | $(GO) run $(TPARSE) -all'

.PHONY: cover
cover: ## Write a unit-test coverage profile, print the total, and open coverage.html
	$(GO) test -short -covermode=atomic -coverprofile=$(COVERPROFILE) ./...
	@$(GO) tool cover -func=$(COVERPROFILE) | tail -n1
	@$(GO) tool cover -html=$(COVERPROFILE) -o coverage.html && echo ">> wrote coverage.html"

.PHONY: test-integration
test-integration: ## Run integration tests (requires docker)
	$(GO) test -race -count=1 ./internal/tests/...

.PHONY: cover-integration
cover-integration: ## Integration coverage (docker): credits coverage to every package the tests exercise
	$(GO) test -count=1 -coverpkg=./... -covermode=atomic -coverprofile=$(COVERPROFILE) ./internal/tests/...
	@$(GO) tool cover -func=$(COVERPROFILE) | tail -n1
	@$(GO) tool cover -html=$(COVERPROFILE) -o coverage.html && echo ">> wrote coverage.html"

.PHONY: cover-check
cover-check: ## Fail if integration coverage is below THRESHOLD% (CI gate; docker; override THRESHOLD=NN)
	$(GO) test -count=1 -coverpkg=./... -covermode=atomic -coverprofile=$(COVERPROFILE) ./internal/tests/...
	@total=$$($(GO) tool cover -func=$(COVERPROFILE) | awk '/^total:/ {print $$3}' | tr -d '%'); \
	if awk "BEGIN { exit !($$total + 0 >= $(THRESHOLD)) }"; then \
		echo ">> coverage $$total% >= $(THRESHOLD)% — OK"; \
	else \
		echo ">> coverage $$total% < $(THRESHOLD)% — FAIL"; exit 1; \
	fi

# ---------------------------------------------------------------------------
# Local infrastructure (Postgres for `make run` / `make migrate`)
# ---------------------------------------------------------------------------
.PHONY: up
up: ## Start local dependencies (docker compose up -d)
	docker compose up -d

.PHONY: down
down: ## Stop local dependencies (docker compose down)
	docker compose down

# ---------------------------------------------------------------------------
# Database migrations (reads config from .env, like the app)
# ---------------------------------------------------------------------------
.PHONY: migrate
migrate: ## Apply all migrations (up)
	$(GO) run . migrate up

.PHONY: migrate-down
migrate-down: ## Roll back one migration
	$(GO) run . migrate down

.PHONY: migrate-status
migrate-status: ## Show migration status
	$(GO) run . migrate status

# ---------------------------------------------------------------------------
# Protobuf / gRPC codegen
# ---------------------------------------------------------------------------
# Baseline the wire contract is diffed against. Defaults to the latest release
# tag; falls back to the `main` branch when there is no tag yet. Override, e.g.
#   make breaking AGAINST='.git#branch=main,subdir=proto'
BUF_BASELINE ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo main)
AGAINST      ?= .git#subdir=proto,ref=$(BUF_BASELINE)

.PHONY: proto
proto: ## Generate gRPC code from .proto (run proto-tools first)
	buf lint proto && buf generate proto

.PHONY: breaking
breaking: ## Detect breaking changes in the gRPC/proto contract vs $(BUF_BASELINE)
	buf breaking proto --against '$(AGAINST)'

.PHONY: openapi
openapi: ## Generate the OpenAPI 3.1 REST spec from swag annotations -> docs/
	$(GO) run $(SWAG) init $(SWAG_FLAGS)

.PHONY: breaking-rest
breaking-rest: openapi ## Detect breaking REST changes (oasdiff) vs the spec at the latest tag
	@cur=$$(git tag --list 'v*' | sort -V | tail -1); \
	if [ -z "$$cur" ]; then echo ">> no tag yet — REST baseline unavailable, skipping"; exit 0; fi; \
	base=$$(mktemp); \
	if ! git show $$cur:$(SPEC) > $$base 2>/dev/null; then echo ">> no $(SPEC) at $$cur, skipping"; exit 0; fi; \
	$(GO) run $(OASDIFF) breaking $$base $(SPEC)

.PHONY: proto-tools
proto-tools: ## Install protobuf codegen tools (buf, protoc-gen-go, protoc-gen-go-grpc)
	$(GO) install github.com/bufbuild/buf/cmd/buf@latest
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

.PHONY: tools
tools: proto-tools ## Install all dev tools (lint, fmt, proto, release/contract diff, swag)
	$(GO) install $(GOLANGCI)
	$(GO) install $(GOFUMPT)
	$(GO) install $(GORELEASE)
	$(GO) install $(SWAG)
	$(GO) install $(OASDIFF)
	$(GO) install $(TPARSE)
	@echo "installed: golangci-lint, gofumpt, gorelease, swag, oasdiff, tparse (+ proto tools)"

# ---------------------------------------------------------------------------
# Release & contract tracking
#
# The contract this service exposes to others is threefold, each with its own
# diff gate vs the latest tag:
#   - Go API  : gorelease — only api/, core/, gen/ are public (the rest is under
#               internal/), so the module-wide diff is scoped to the contract.
#   - gRPC    : buf breaking            (make breaking)
#   - REST    : oasdiff on the OpenAPI  (make breaking-rest)
#
# release-suggest aggregates all three; release-auto tags gorelease's suggestion
# but refuses when a wire gate (gRPC/REST) reports a breaking change (those
# require a deliberate major). check-version enforces one semver step.
# ---------------------------------------------------------------------------
.PHONY: gorelease
gorelease: ## Go-API diff of the public packages (api/, core/, gen/) vs the latest tag
	$(GO) run $(GORELEASE)

.PHONY: release-suggest
release-suggest: openapi ## Suggest the next version from the Go + gRPC + REST contract diffs
	@echo "── Go API (gorelease) ──";   $(GO) run $(GORELEASE) || true
	@echo "── gRPC (buf breaking) ──";  $(MAKE) -s breaking      && echo "  gRPC: compatible" || echo "  gRPC: BREAKING -> major"
	@echo "── REST (oasdiff) ──";       $(MAKE) -s breaking-rest && echo "  REST: compatible" || echo "  REST: BREAKING -> major"
	@echo "Pick gorelease's suggestion, OR a major bump if any wire gate is BREAKING."

.PHONY: release-auto
release-auto: check-clean openapi ## Tag & push gorelease's suggested version (refuses on a wire-contract break)
	@$(MAKE) -s breaking      || { echo "gRPC contract broke — bump a MAJOR: make release V=vX.0.0"; exit 1; }
	@$(MAKE) -s breaking-rest || { echo "REST contract broke — bump a MAJOR: make release V=vX.0.0"; exit 1; }
	@v=$$($(GO) run $(GORELEASE) 2>/dev/null | sed -n 's/^Suggested version: //p'); \
	if [ -z "$$v" ]; then \
		echo "gorelease gave no suggestion (incompatible Go API or first release) — use make release V=..."; exit 1; \
	fi; \
	echo ">> gorelease suggests $$v"; $(MAKE) release V=$$v

.PHONY: check-clean
check-clean:
	@test -z "$$(git status --porcelain)" || \
		{ echo "working tree is dirty — commit (or stash) changes before releasing"; \
		  git status --short; exit 1; }

.PHONY: check-version
check-version:
	@test -n "$(V)" || { echo "usage: make release V=vX.Y.Z"; exit 1; }
	@echo "$(V)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$$' || \
		{ echo "V must be vX.Y.Z (no prerelease/build suffix): got $(V)"; exit 1; }
	@cur=$$(git tag --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$$' | sort -V | tail -n1); \
	if [ -z "$$cur" ]; then \
		echo ">> first release ($(V)); no prior tag to compare against"; \
	else \
		cv=$${cur#v}; cM=$${cv%%.*}; cr=$${cv#*.}; cm=$${cr%%.*}; cp=$${cr##*.}; \
		np="v$$cM.$$cm.$$((cp + 1))"; nm="v$$cM.$$((cm + 1)).0"; nj="v$$((cM + 1)).0.0"; \
		case "$(V)" in \
			"$$np"|"$$nm"|"$$nj") echo ">> $(V) is exactly one step above $$cur" ;; \
			*) echo "ERROR: $(V) must be exactly one step above the latest tag $$cur"; \
			   echo "       allowed: $$np (patch) | $$nm (minor) | $$nj (major)"; exit 1 ;; \
		esac; \
	fi

.PHONY: release
release: check-clean check-version ## Tag & push a release: make release V=v0.1.0
	@echo ">> verifying build & tests"; $(GO) build ./... && $(GO) test -short ./...
	@echo ">> tagging $(V)"; git tag -a $(V) -m "Release $(V)"
	@echo ">> pushing $(V)"; git push origin $(V)
