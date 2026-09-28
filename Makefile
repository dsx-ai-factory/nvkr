# nkdctl — Definition -> Run -> Release tooling.
# Run `make help` for targets; `make check` is the full local completion gate.

BIN         := bin/nkdctl
PKG         := ./...
# Module source dirs only (never the vendored trees checked out under .nkd/).
GOSRC       := client cmd internal pkg
GOFILES     := $(shell find $(GOSRC) -type f -name '*.go' -print)
GO_VERSION  := $(shell cat .go-version 2>/dev/null)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# Only version+commit are stamped (both git-derived, deterministic) — no build
# date, so the tool binary itself stays reproducible in nkdctl's own spirit.
LDFLAGS     := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Authority-bearing build profiles pin their own toolchain identity. For the
# repository dev loop, GOTOOLCHAIN=local prevents an implicit toolchain download;
# .go-version records the supported major/minor used by CI, not an exact local
# compiler-byte identity. Override to let `go` fetch that CI version explicitly.
GOTOOLCHAIN ?= local
export GOTOOLCHAIN

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN{FS=":.*## "}/^[a-zA-Z0-9_-]+:.*## /{printf "  \033[36m%-13s\033[0m %s\n",$$1,$$2}' $(MAKEFILE_LIST)

.PHONY: info
info: ## Print version / toolchain info.
	@echo "version:     $(VERSION)"
	@echo "commit:      $(COMMIT)"
	@echo "go:          $(GO_VERSION)"
	@echo "gotoolchain: $(GOTOOLCHAIN)"

# --- build ------------------------------------------------------------------

.PHONY: build
build: ## Compile the nkdctl binary.
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/nkdctl

.PHONY: install
install: ## Install nkdctl into $$(go env GOBIN).
	go install -ldflags '$(LDFLAGS)' ./cmd/nkdctl

.PHONY: nkd-exec-helper
nkd-exec-helper: ## Reproducibly build the ADR-18 nkd-exec helper binaries + write the lock (ARGS=--image also assembles the image).
	@bash toolchains/nkd-exec/build-nkd-exec.sh $(ARGS)

# --- quality ----------------------------------------------------------------

.PHONY: fmt
fmt: ## Format code (gofmt -s -w).
	gofmt -s -w $(GOFILES)

.PHONY: fmt-check
fmt-check: ## Check formatting (CI-friendly, no writes).
	@test -z "$$(gofmt -l $(GOFILES))" || { echo "unformatted files — run 'make fmt':"; gofmt -l $(GOFILES); exit 1; }
	@echo "formatting ok"

.PHONY: vet
vet: ## Run go vet.
	go vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint (see .golangci.yml; install: https://golangci-lint.run).
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed — https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run

.PHONY: shellcheck
shellcheck: ## Check executable shell surfaces for warning-or-higher defects.
	@command -v shellcheck >/dev/null 2>&1 || { echo "shellcheck not installed — https://www.shellcheck.net/"; exit 1; }
	shellcheck -S warning scripts/*.sh toolchains/**/*.sh

.PHONY: vulncheck
vulncheck: ## Check reachable Go vulnerabilities using the pinned upstream scanner.
	go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

.PHONY: test
test: ## Unit tests with race + coverage (pure logic; no docker required).
	go test -race -covermode=atomic -coverprofile=coverage.out $(PKG)
	@go tool cover -func=coverage.out | tail -1

.PHONY: cover
cover: test ## Open the HTML coverage report in a browser.
	go tool cover -html=coverage.out

.PHONY: tidy
tidy: ## Format + go mod tidy.
	gofmt -s -w $(GOFILES)
	go mod tidy

.PHONY: notices
notices: ## Regenerate THIRD_PARTY_NOTICES.md from the linked module set.
	./scripts/gen-notices.sh

.PHONY: notices-check
notices-check: ## Verify THIRD_PARTY_NOTICES.md matches the linked module set (drift gate).
	./scripts/gen-notices.sh --check

.PHONY: workflows-check
workflows-check: ## Verify the committed Run generation matches the catalog (ADR-21 drift gate).
	@# The generator version is an input to regeneration (ADR-21 §1) and is recorded
	@# in .github/nkd-run/plan.json as `generatorVersion`. Building via `make build`,
	@# whose version comes from `git describe`, therefore always reports drift. Build
	@# the generator with the same canonical version string the generated Run
	@# workflow uses, so this target is reproducible locally and in CI.
	CGO_ENABLED=0 go build -trimpath \
		-ldflags '-X main.version=dev -X main.commit=unknown' \
		-o bin/nkd-exec ./cmd/nkdctl
	bin/nkd-exec run generate --engine github \
		--definition definitions/nke-v1/definition.yaml \
		--environment definitions/nke-v1/environment-github.yaml \
		--root . --check

.PHONY: check
check: fmt-check vet lint shellcheck test vulncheck ## Full local gate — run before pushing.

# --- housekeeping -----------------------------------------------------------

.PHONY: clean
clean: ## Remove build output, coverage, and the local engine workspace.
	go clean
	rm -rf bin coverage.out .nkd/work

.PHONY: run-resolve
run-resolve: build ## Resolve the pinned kubectl producer and print its build graph.
	$(BIN) resolve --data testdata/catalogs/kubernetes-layered --component kubectl --platform linux/amd64
