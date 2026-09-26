BINARY := pit
PKG    := github.com/thannoz/pit
BIN    := bin/$(BINARY)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/cli.version=$(VERSION) \
	-X $(PKG)/internal/cli.commit=$(COMMIT) \
	-X $(PKG)/internal/cli.date=$(DATE)

.DEFAULT_GOAL := check

.PHONY: build
build: ## Build the binary into bin/
	@mkdir -p bin
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/$(BINARY)

.PHONY: install
install: ## Install the binary into GOPATH/bin
	go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/$(BINARY)

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: test-offline
test-offline: ## Run the tests with all network access blocked (T-106)
	@echo "Running the suite with git restricted to local paths and no module proxy."
	env -i PATH="$$PATH" HOME="$$HOME" \
		GOMODCACHE="$$(go env GOMODCACHE)" GOCACHE="$$(go env GOCACHE)" \
		$${SYSTEMROOT:+SYSTEMROOT="$$SYSTEMROOT"} $${TEMP:+TEMP="$$TEMP" TMP="$$TEMP"} \
		$${USERPROFILE:+USERPROFILE="$$USERPROFILE"} $${LOCALAPPDATA:+LOCALAPPDATA="$$LOCALAPPDATA"} \
		GOPROXY=off \
		GIT_ALLOW_PROTOCOL=file \
		GIT_TERMINAL_PROMPT=0 \
		HTTP_PROXY=http://127.0.0.1:1 \
		HTTPS_PROXY=http://127.0.0.1:1 \
		ALL_PROXY=socks5://127.0.0.1:1 \
		NO_PROXY= \
		go test -count=1 ./...

.PHONY: cover
cover: ## Run tests and open the coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -l -w .

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint-actions
lint-actions: ## Check the workflows, including the one shipped in examples/
	@command -v actionlint >/dev/null 2>&1 || { \
		echo "actionlint not installed: go install github.com/rhysd/actionlint/cmd/actionlint@latest"; exit 1; }
	actionlint .github/workflows/*.yml examples/github-actions/*.yml

.PHONY: lint
lint: ## Run golangci-lint (see T-006)
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not installed: brew install golangci-lint"; exit 1; }
	golangci-lint run

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: check
check: vet test build ## Vet, test and build

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out

.PHONY: help
help: ## List the available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
