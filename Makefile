GOLANGCI_VERSION := $(shell cat .golangci-lint-version)
IMAGE ?= ghcr.io/jcwearn/hivemind

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: generate
generate: ## Regenerate templ output
	go tool templ generate

.PHONY: build
build: generate ## Build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -o bin/hivemind ./cmd/hivemind

.PHONY: run
run: generate ## Run locally on :8080
	go run ./cmd/hivemind

# The address matters here. On localhost the QR encodes localhost, which is
# useless on a phone -- so this prints the LAN address to scan instead. This is
# the target you actually want when testing with real phones.
.PHONY: party
party: generate ## Run on the LAN so phones can scan the QR
	@ip=$$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $$1}'); \
	if [ -z "$$ip" ]; then echo "could not work out a LAN address"; exit 1; fi; \
	echo "hivemind on http://$$ip:8080"; \
	HIVEMIND_BASE_URL=http://$$ip:8080 go run ./cmd/hivemind

.PHONY: test
test: generate ## Run the tests with the race detector
	go test -race ./...

.PHONY: cover
cover: generate ## Write and open a coverage report
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

# Uses the same version CI does, read from the same file, so "passes locally"
# and "passes in CI" mean the same thing.
.PHONY: lint
lint: generate ## Lint with the pinned golangci-lint
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not installed. Wanted v$(GOLANGCI_VERSION):"; \
		echo "  brew install golangci-lint   # or see https://golangci-lint.run/docs/welcome/install/"; \
		exit 1; }
	@have=$$(golangci-lint version --short 2>/dev/null || golangci-lint --version | awk '{print $$4}'); \
	case "$$have" in \
		$(GOLANGCI_VERSION)*) ;; \
		*) echo "warning: golangci-lint $$have installed, .golangci-lint-version pins $(GOLANGCI_VERSION)" ;; \
	esac
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Rewrite formatting in place
	go tool templ fmt .
	golangci-lint fmt

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: docker-build
docker-build: ## Build the container image
	docker build -t $(IMAGE):dev .

.PHONY: docker-run
docker-run: docker-build ## Run the image on :8080
	docker run --rm -p 8080:8080 -e HIVEMIND_BASE_URL=http://localhost:8080 $(IMAGE):dev

# htmx is vendored with its version in the filename, so the path is the pin and
# a bump means a new file plus the two <script> tags in layout.templ. Renovate
# deliberately does NOT manage this: a regex manager could rewrite the version
# string in the template, but it cannot rename a file, so every bump it opened
# would be a broken build. A few times a year, by hand, is the honest answer.
HTMX_VERSION ?= 2.0.10
HTMX_SSE_VERSION ?= 2.2.4

.PHONY: vendor-htmx
vendor-htmx: ## Re-vendor htmx (set HTMX_VERSION / HTMX_SSE_VERSION)
	@set -euo pipefail; \
	curl -fsSL -o static/vendor/htmx-$(HTMX_VERSION).min.js \
		https://unpkg.com/htmx.org@$(HTMX_VERSION)/dist/htmx.min.js; \
	curl -fsSL -o static/vendor/htmx-ext-sse-$(HTMX_SSE_VERSION).js \
		https://unpkg.com/htmx-ext-sse@$(HTMX_SSE_VERSION)/sse.js; \
	echo "vendored htmx $(HTMX_VERSION) and sse $(HTMX_SSE_VERSION)"; \
	echo "now update the <script> tags in internal/ui/layout.templ,"; \
	echo "the cache-control test in internal/web/server_test.go,"; \
	echo "and delete the files these replace."

.PHONY: clean
clean: ## Remove build output
	rm -rf bin coverage.out coverage.html
