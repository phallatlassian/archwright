BIN_DIR := bin
BIN     := $(BIN_DIR)/archwright
PKG     := ./cmd/archwright
COVER   := $(BIN_DIR)/coverage.out

.DEFAULT_GOAL := help

.PHONY: help build test vet fmt fmt-check tidy clean run cover lint

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

build: ## Build the archwright binary into bin/
	go build -o $(BIN) $(PKG)

test: ## Run all tests
	go test ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go code
	gofmt -w .

fmt-check: ## Fail if any Go code is unformatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

tidy: ## Run go mod tidy
	go mod tidy

clean: ## Remove build output
	rm -rf $(BIN_DIR)

run: ## Run archwright (pass args with ARGS="...")
	go run $(PKG) $(ARGS)

cover: ## Run tests with coverage; writes bin/coverage.out
	@mkdir -p $(BIN_DIR)
	go test -coverprofile=$(COVER) ./...
	go tool cover -func=$(COVER) | tail -n 1

lint: ## Run golangci-lint (optional; skipped if not installed)
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else echo "golangci-lint not installed; skipping"; fi
