# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# nkdctl — Definition -> Run -> Release tooling.
# Run `make help` for targets; `make check` is the full local completion gate.

BIN         := bin/nkdctl
PKG         := ./...
# Optional diagnostic flags, e.g. GO_TEST_FLAGS=-json for CI timing capture.
GO_TEST_FLAGS ?=
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
# .go-version pins the exact Go patch used by CI. Keep local builds and scans
# on that patch without implicitly downloading a toolchain.
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
build: go-version-check ## Compile the nkdctl binary.
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/nkdctl

.PHONY: install
install: ## Install nkdctl into $$(go env GOBIN).
	go install -ldflags '$(LDFLAGS)' ./cmd/nkdctl

.PHONY: nkd-exec-helper
nkd-exec-helper: ## Reproducibly build the ADR-18 nkd-exec helper binaries + write the lock (ARGS=--image also assembles the image).
	@bash toolchains/nkd-exec/build-nkd-exec.sh $(ARGS)

# --- quality ----------------------------------------------------------------

.PHONY: go-version-check
go-version-check: ## Require the exact Go patch pinned in .go-version.
	@actual="$$(go env GOVERSION)"; expected="go$(GO_VERSION)"; \
	  test "$$actual" = "$$expected" || { echo "Go $$actual does not match pinned $$expected" >&2; exit 1; }

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
vulncheck: go-version-check ## Check reachable Go vulnerabilities using the pinned upstream scanner.
	GOBIN="$(CURDIR)/bin" go -C toolchains/govulncheck install tool
	"$(CURDIR)/bin/govulncheck" ./...

.PHONY: test
test: ## Unit tests with race + coverage (pure logic; no docker required).
	# Full factory race/coverage contracts exceed Go's default 10m package budget on hosted runners.
	go test -timeout 20m -race -covermode=atomic -coverprofile=coverage.out $(GO_TEST_FLAGS) $(PKG)
	@go tool cover -func=coverage.out | tail -1

.PHONY: test-ci-tools
test-ci-tools: ## Check CI timing report parsing and failure diagnostics.
	python3 -B -m unittest discover -s scripts -p ci_test_summary_test.py

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

ADDLICENSE_VERSION ?= v1.1.1
LICENSE_HOLDER     := NVIDIA CORPORATION & AFFILIATES
# Not stamped: generated or machine-managed files, files tests compare byte for
# byte, recorded evidence and example data, templates that emit workflows, and
# build output. Keep LICENSE_UNCHECKED in step with LICENSE_IGNORES.
LICENSE_IGNORES := -ignore '**/testdata/**' -ignore '**/pins.lock.yaml' \
	-ignore '.github/workflows/nkd-run.yml' -ignore '**/*.tmpl' \
	-ignore 'docs/evidence/**' -ignore 'docs/**/*.yaml' -ignore 'docs/**/*.yml' \
	-ignore '.nkd/**' -ignore '.nkd-inputs/**' -ignore 'bin/**'
LICENSE_UNCHECKED := (^|/)testdata/|pins\.lock\.yaml$$|^\.github/workflows/nkd-run\.yml$$|\.tmpl$$|^docs/evidence/|^docs/.*\.ya?ml$$|^\.nkd(-inputs)?/|^bin/
ADDLICENSE := go run github.com/google/addlicense@$(ADDLICENSE_VERSION)

.PHONY: license
license: ## Add the SPDX license header to source files that lack one.
	$(ADDLICENSE) -f scripts/license-header.tmpl -c "$(LICENSE_HOLDER)" $(LICENSE_IGNORES) .

.PHONY: license-header-check
license-header-check: ## Fail when a source file has no license header.
	$(ADDLICENSE) -check -f scripts/license-header.tmpl -c "$(LICENSE_HOLDER)" $(LICENSE_IGNORES) .
	@# addlicense accepts a file that merely mentions "copyright" near the top, so also
	@# require a license line in the first 20 lines of every tracked source file.
	@missing=$$(git ls-files '*.go' '*.py' '*.sh' '*.yaml' '*.yml' '*Dockerfile*' \
		| grep -v -E '$(LICENSE_UNCHECKED)' \
		| while read -r f; do \
			head -20 "$$f" | grep -q -E 'SPDX-License-Identifier|Licensed under the Apache License' || echo "$$f"; \
		done); \
	 [ -z "$$missing" ] || { echo "ERROR: no license line in the first 20 lines of:"; echo "$$missing" | sed 's/^/    /'; exit 1; }

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
		--definition definitions/nke-m2/definition.yaml \
		--environment definitions/nke-m2/environment-github.yaml \
		--root . --check

.PHONY: check
check: go-version-check fmt-check license-header-check vet lint shellcheck test-ci-tools test vulncheck ## Full local gate — run before pushing.

# --- housekeeping -----------------------------------------------------------

.PHONY: clean
clean: ## Remove build output, coverage, and the local engine workspace.
	go clean
	rm -rf bin coverage.out .nkd/work

.PHONY: run-resolve
run-resolve: build ## Resolve the pinned kubectl producer and print its build graph.
	$(BIN) resolve --data testdata/catalogs/kubernetes-layered --component kubectl --platform linux/amd64
