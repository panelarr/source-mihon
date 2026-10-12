# Self-documenting Makefile: `make` or `make help` lists every target with a `## description`.
.DEFAULT_GOAL := help

# Developer tools are pinned here and installed into ./bin by `make setup`.
BUF_VERSION                   := v1.73.0
PROTOC_GEN_GO_VERSION         := v1.36.12
PROTOC_GEN_CONNECT_GO_VERSION := v1.21.0
GOLANGCI_LINT_VERSION         := v2.14.0
GOVULNCHECK_VERSION           := v1.9.0

BIN      := $(CURDIR)/bin
export PATH := $(BIN):$(PATH)
GO       ?= go
# Build tools with the same Go version as the module, so linters understand its code.
TOOLCHAIN := $(shell $(GO) env GOVERSION)
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X github.com/panelarr/source-mihon/internal/buildinfo.Version=$(VERSION)
IMAGE    ?= panelarr/source-mihon:dev
SOURCE_API_DIR  ?= ../source-api
MIHON_SOURCE_DIR ?= ../mihon-source

.PHONY: help setup build test test-short lint fmt fmt-check vet vuln gen gen-check pin-check sync-mihon-protos run e2e docker clean check

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_.-]+:.*## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: ## Install pinned developer tools into ./bin and download modules
	@mkdir -p $(BIN)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install connectrpc.com/connect/cmd/protoc-gen-connect-go@$(PROTOC_GEN_CONNECT_GO_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) mod download

build: ## Build the plugin binary into ./bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/source-mihon ./cmd/source-mihon

test: ## Run all tests with the race detector (the end-to-end test runs only with make e2e)
	$(GO) test -race -cover ./...

test-short: ## Run the fast tests only
	$(GO) test -short -race ./...

fmt: ## Format Go files
	$(GO) fmt ./...

fmt-check: ## Fail if Go files are not formatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt-ed:"; echo "$$out"; exit 1; fi

vet: ## go vet
	$(GO) vet ./...

lint: ## golangci-lint
	$(BIN)/golangci-lint run ./...

vuln: ## Check dependencies and the toolchain for known vulnerabilities
	$(BIN)/govulncheck ./...

gen: ## Generate Go code for the vendored mihon-source protos
	$(BIN)/buf generate

gen-check: gen ## Fail if generated code is out of date
	@if [ -n "$$(git status --porcelain -- gen)" ]; then git status --short -- gen; echo "generated code is stale: run make gen and commit"; exit 1; fi

pin-check: ## Fail if the vendored mihon-source protos differ from their pin
	@cd third_party/mihon-source && for f in proto/panelarr/mihon/v1/*.proto; do \
		want=$$(grep "^sha256.$$(basename $$f)=" PIN | cut -d= -f2); \
		got=$$(shasum -a 256 $$f | cut -d' ' -f1); \
		if [ "$$want" != "$$got" ]; then echo "third_party/mihon-source/$$f was edited: change it in mihon-source, then run make sync-mihon-protos"; exit 1; fi; \
	done

sync-mihon-protos: ## Pin the mihon-source protos from a checkout: make sync-mihon-protos [REF=main] [MIHON_SOURCE_DIR=../mihon-source]
	@commit=$$(git -C $(MIHON_SOURCE_DIR) rev-parse "$(or $(REF),main)^{commit}") && \
	grep -E '^(#|repository=)' third_party/mihon-source/PIN > third_party/mihon-source/PIN.new && \
	echo "commit=$$commit" >> third_party/mihon-source/PIN.new && \
	for f in extensions stores; do \
		git -C $(MIHON_SOURCE_DIR) show $$commit:proto/panelarr/mihon/v1/$$f.proto > third_party/mihon-source/proto/panelarr/mihon/v1/$$f.proto; \
		echo "sha256.$$f.proto=$$(shasum -a 256 third_party/mihon-source/proto/panelarr/mihon/v1/$$f.proto | cut -d' ' -f1)" >> third_party/mihon-source/PIN.new; \
	done && mv third_party/mihon-source/PIN.new third_party/mihon-source/PIN && echo "pinned mihon-source $$commit; run make gen"

run: build ## Run the plugin standalone on :8904 against MIHON_SOURCE_URL (default http://localhost:8902)
	@echo "source-mihon listening on http://localhost:8904"
	MIHON_SOURCE_URL=$${MIHON_SOURCE_URL:-http://localhost:8902} $(BIN)/source-mihon --listen :8904

e2e: ## End-to-end: real mihon-source (built from MIHON_SOURCE_DIR) with its test extension and fixture store, through this plugin, under conformance
	cd $(MIHON_SOURCE_DIR) && ./gradlew --no-watch-fs -q :service:installDist :test-extensions:lib16:jar :service:exportFixtureStore -PfixtureStoreDir=$(abspath $(MIHON_SOURCE_DIR))/service/build/fixture-store
	SOURCE_MIHON_E2E_MIHON_SOURCE=$(abspath $(MIHON_SOURCE_DIR))/service/build/install/mihon-source/bin/mihon-source \
	SOURCE_MIHON_E2E_EXTENSION=$$(ls $(abspath $(MIHON_SOURCE_DIR))/test-extensions/lib16/build/libs/lib16-*.jar | head -1) \
	SOURCE_MIHON_E2E_STORE=$(abspath $(MIHON_SOURCE_DIR))/service/build/fixture-store \
	$(GO) test -count=1 -v -run TestEndToEnd ./internal/e2e/

docker: ## Build the container image ($(IMAGE)); needs the source-api checkout in SOURCE_API_DIR (default ../source-api)
	@test -f "$(SOURCE_API_DIR)/go.mod" || (echo "source-api checkout not found at $(SOURCE_API_DIR); clone it next to this repo or set SOURCE_API_DIR" && exit 2)
	docker build --build-context source-api=$(SOURCE_API_DIR) --build-arg VERSION=$(VERSION) -t $(IMAGE) .

clean: ## Remove build artifacts (keeps installed tools)
	rm -f $(BIN)/source-mihon
	rm -rf dist coverage.out

check: fmt-check vet lint pin-check vuln test ## Format check, lint, the proto pin, vulnerabilities and tests (run before every report/PR)
