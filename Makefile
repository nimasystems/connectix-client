GO           ?= go
STATICCHECK  ?= honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK  ?= golang.org/x/vuln/cmd/govulncheck@v1.9.0
MIN_COVERAGE ?= 90

.DEFAULT_GOAL := check

.PHONY: help
help: ## List the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-10s %s\n", $$1, $$2}'

.PHONY: check
check: fmt vet lint vuln test cover ## Run everything CI runs (fmt, vet, staticcheck, govulncheck, race tests, coverage floor)

.PHONY: test
test: ## Run the tests with the race detector
	$(GO) test -race -count=1 ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Fail if any file is not gofmt'd
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

.PHONY: fmt-fix
fmt-fix: ## Rewrite files with gofmt
	gofmt -w .

.PHONY: cover
cover: ## Run the tests with coverage and fail below MIN_COVERAGE percent
	$(GO) test -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	@total="$$($(GO) tool cover -func=coverage.out | awk '/^total:/ {sub("%", "", $$3); print $$3}')"; \
	echo "coverage: $$total% (floor $(MIN_COVERAGE)%)"; \
	awk -v t="$$total" -v m="$(MIN_COVERAGE)" 'BEGIN { exit (t + 0 < m + 0) }' || { echo "coverage below $(MIN_COVERAGE)%"; exit 1; }

.PHONY: lint
lint: ## Run staticcheck (fetched by go run; needs network the first time)
	$(GO) run $(STATICCHECK) ./...

.PHONY: vuln
vuln: ## Run govulncheck against the code and the Go standard library in use
	$(GO) run $(GOVULNCHECK) ./...
