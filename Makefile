# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# nvkr — Definition -> Run -> Release tooling.
# Run `make help` for targets; `make check` is the full local completion gate.

BIN         := bin/nvkr
DEMO_BIN    ?= bin/nvke-demo
DEMO_TEST_BIN ?= bin/nvke-demo.test
PKG         := ./...
# Optional diagnostic flags, e.g. GO_TEST_FLAGS=-json for CI timing capture.
GO_TEST_FLAGS ?=
# Bound each package explicitly; the local unsharded suite can exceed Go's
# implicit 10-minute default. Hosted shards have a separate five-minute job budget.
GO_TEST_TIMEOUT ?= 20m
# CI splits the ordinary suite; reproduce any matrix entry with make test-shard.
TEST_SHARD_GROUP ?=
TEST_SHARD_INDEX ?=
# Dispatch is explicit about the repository: a checkout may have a fork and upstream.
PIPELINE_DEV_REPO ?=
export PIPELINE_DEV_REPO
# Module source dirs only (never the vendored trees checked out under .nvkr/).
GOSRC       := client cmd internal pkg
GO_VERSION  := $(shell cat .go-version 2>/dev/null)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# Only version+commit are stamped (both git-derived, deterministic) — no build
# date, so the tool binary itself stays reproducible in nvkr's own spirit.
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
build: go-version-check ## Compile the nvkr binary.
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/nvkr
	chmod 0755 $(BIN)

.PHONY: demo demo-test-bin
demo: go-version-check ## Build the downstream Go local NVKE demo CLI (calls nvkr).
	go build -o $(DEMO_BIN) ./cmd/nvke-demo
	chmod 0755 $(DEMO_BIN)

demo-test-bin: go-version-check ## Compile opt-in native NVKE E2E tests; does not run them.
	go test -c -o $(DEMO_TEST_BIN) ./internal/nvkedemo

.PHONY: install
install: ## Install nvkr into $$(go env GOBIN).
	go install -ldflags '$(LDFLAGS)' ./cmd/nvkr

.PHONY: nvkr-exec-helper
nvkr-exec-helper: ## Reproducibly build the ADR-18 nvkr-exec helper binaries + write the lock (ARGS=--image also assembles the image).
	@bash toolchains/nvkr-exec/build-nvkr-exec.sh $(ARGS)

# --- quality ----------------------------------------------------------------

.PHONY: go-version-check
go-version-check: ## Require the exact Go patch pinned in .go-version.
	@actual="$$(go env GOVERSION)"; expected="go$(GO_VERSION)"; \
	  test "$$actual" = "$$expected" || { echo "Go $$actual does not match pinned $$expected" >&2; exit 1; }

.PHONY: fmt
fmt: ## Format code (gofmt -s -w).
	gofmt -s -w $(GOSRC)

.PHONY: fmt-check
fmt-check: ## Check formatting (CI-friendly, no writes).
	@unformatted="$$(gofmt -l $(GOSRC))" || exit $$?; \
	  test -z "$$unformatted" || { echo "unformatted files — run 'make fmt':"; printf '%s\n' "$$unformatted"; exit 1; }
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
	shellcheck -S warning scripts/*.sh demos/*.sh toolchains/**/*.sh

.PHONY: vulncheck
vulncheck: go-version-check ## Check reachable Go vulnerabilities using the pinned upstream scanner.
	GOBIN="$(CURDIR)/bin" go -C toolchains/govulncheck install tool
	"$(CURDIR)/bin/govulncheck" ./...

.PHONY: test
test: ## Fast default tests for CI and agents (no extended fixtures or instrumentation).
	go test -timeout $(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS) $(PKG)

.PHONY: test-shard
test-shard: go-version-check ## Reproduce one CI test shard (TEST_SHARD_GROUP and TEST_SHARD_INDEX required).
	python3 -B scripts/ci-test-shard.py --group '$(TEST_SHARD_GROUP)' --index '$(TEST_SHARD_INDEX)' --timeout '$(GO_TEST_TIMEOUT)'

.PHONY: test-race
test-race: ## Opt-in race detection and coverage for the default suite.
	go test -timeout 20m -race -covermode=atomic -coverprofile=coverage.out $(GO_TEST_FLAGS) $(PKG)
	@go tool cover -func=coverage.out | tail -1

.PHONY: test-full
test-full: ## Include extended capacity/integration fixtures with race + coverage.
	NVKR_TEST_EXTENDED=1 $(MAKE) test-race

.PHONY: test-ci-tools
test-ci-tools: ## Require Python and check CI sharding, reports and local adapters.
	python3 -B -c 'import sys; sys.exit(0 if sys.version_info >= (3, 12) else "Python 3.12+ is required for CI report tests")'
	python3 -B -m unittest discover -s scripts -p '*_test.py'

.PHONY: cover
cover: test-race ## Open the HTML coverage report in a browser.
	go tool cover -html=coverage.out

.PHONY: tidy
tidy: ## Format + go mod tidy.
	gofmt -s -w $(GOSRC)
	go mod tidy

.PHONY: notices
notices: ## Regenerate THIRD_PARTY_NOTICES.md from the linked module set.
	./scripts/gen-notices.sh

.PHONY: notices-check
notices-check: ## Verify THIRD_PARTY_NOTICES.md matches the linked module set (drift gate).
	./scripts/gen-notices.sh --check

# Only the pages listed in docs/index.yml are published to the Fern site; contributor,
# ADR and evidence documents stay on GitHub and are not checked here. Fern is
# deliberately unpinned: install it with `npm install -g fern-api@latest`.
#
# The Fern checks run on an isolated copy holding only index.yml, the listed pages and
# fern/ without its registered versions. A link from a published page to anything else
# in the repository (an unpublished page, source, a script) therefore fails here the
# way it is a dead link on the site, instead of resolving against the full checkout.
# Registered release snapshots are materialized only when publishing, so they are
# left out of this check. `broken-links --strict` is required: without it Fern prints
# the errors and still exits 0.
DOCS_PAGES := $(shell sed -n 's|^ *path: \(.*\.md\)$$|docs/\1|p' docs/index.yml 2>/dev/null)

.PHONY: docs-check
docs-check: ## Validate the published Fern docs (needs node and fern-api).
	@command -v fern >/dev/null 2>&1 || { echo "fern not installed — npm install -g fern-api@latest" >&2; exit 1; }
	@[ -n "$(DOCS_PAGES)" ] || { echo "no pages listed in docs/index.yml" >&2; exit 1; }
	python3 tools/check-docs-mdx $(DOCS_PAGES)
	@set -e; \
	 work="$$(mktemp -d)"; trap 'rm -rf "$$work"' EXIT; \
	 tar -cf - docs/index.yml $(DOCS_PAGES) | tar -xf - -C "$$work"; \
	 mkdir "$$work/fern"; \
	 for f in fern/*; do [ "$${f##*/}" = versions ] || cp -R "$$f" "$$work/fern/"; done; \
	 if grep -q '^ *versions:' "$$work/fern/docs.yml"; then \
	   command -v yq >/dev/null 2>&1 || { echo "yq is required to ignore registered versions in fern/docs.yml" >&2; exit 1; }; \
	   yq -i 'del(.products[].versions)' "$$work/fern/docs.yml"; \
	 fi; \
	 cd "$$work"; \
	 echo "fern check"; fern check; \
	 echo "fern docs md check"; fern docs md check; \
	 echo "fern docs broken-links --strict"; fern docs broken-links --strict

ADDLICENSE_VERSION := v1.1.1
LICENSE_HOLDER     := NVIDIA CORPORATION & AFFILIATES
# Not stamped: generated or machine-managed files, files tests compare byte for
# byte, recorded evidence and example data, templates that emit workflows, and
# build output. Keep LICENSE_UNCHECKED in step with LICENSE_IGNORES.
LICENSE_IGNORES := -ignore '**/testdata/**' -ignore '**/pins.lock.yaml' \
	-ignore '.github/workflows/nvkr-run.yml' -ignore '.github/workflows/nvkr-qualify.yml' \
	-ignore '.github/workflows/nvkr-run-*.yml' \
	-ignore '.github/workflows/nvkr-dev-qualify.yml' -ignore '.github/workflows/nvkr-dev-smoke.yml' \
	-ignore '.github/workflows/nvkr-smoke.yml' -ignore '.github/workflows/nvkr-dev.yml' -ignore '**/*.tmpl' \
	-ignore 'docs/evidence/**' -ignore 'docs/**/*.yaml' -ignore 'docs/**/*.yml' \
	-ignore '.nvkr/**' -ignore '.nvkr-inputs/**' -ignore '.claude/**' -ignore 'bin/**'
LICENSE_UNCHECKED := (^|/)testdata/|pins\.lock\.yaml$$|^\.github/workflows/nvkr-(run(-[a-z0-9-]+)?|qualify|smoke|dev(-qualify|-smoke)?)\.yml$$|\.tmpl$$|^docs/evidence/|^docs/.*\.ya?ml$$|^\.nvkr(-inputs)?/|^\.claude/|^bin/
ADDLICENSE := go run github.com/google/addlicense@$(ADDLICENSE_VERSION)
# addlicense walks every directory under its argument, including untracked ones
# such as other worktrees, so hand it Git's file list instead of ".".
LICENSE_FILES := git ls-files -z --cached --others --exclude-standard

.PHONY: license
license: ## Add the SPDX license header to source files that lack one.
	$(LICENSE_FILES) | xargs -0 $(ADDLICENSE) -f scripts/license-header.tmpl -c "$(LICENSE_HOLDER)" $(LICENSE_IGNORES)

.PHONY: license-header-check
license-header-check: ## Fail when a source file has no license header.
	@$(LICENSE_FILES) | xargs -0 $(ADDLICENSE) -check -f scripts/license-header.tmpl -c "$(LICENSE_HOLDER)" $(LICENSE_IGNORES) \
		|| { echo "ERROR: the files above have no license header; run 'make license'" >&2; exit 1; }
	@# addlicense accepts a file that merely mentions "copyright" near the top, so also
	@# require a license line in the first 20 lines of every tracked source file.
	@missing=$$(git ls-files '*.go' '*.py' '*.sh' '*.yaml' '*.yml' '*.jq' '*Dockerfile*' '*Makefile' \
		| grep -v -E '$(LICENSE_UNCHECKED)' \
		| while read -r f; do \
			head -20 "$$f" | grep -q -E 'SPDX-License-Identifier|Licensed under the Apache License' || echo "$$f"; \
		done); \
	 [ -z "$$missing" ] || { echo "ERROR: no license line in the first 20 lines of:"; echo "$$missing" | sed 's/^/    /'; echo "Run 'make license'; add the header by hand to a Makefile or *.jq." >&2; exit 1; }

.PHONY: workflow-generator
workflow-generator: go-version-check
	@# The generator version is an input to regeneration (ADR-21 §1) and is recorded
	@# in .github/nvkr-run/plan.json as `generatorVersion`. Building via `make build`,
	@# whose version comes from `git describe`, therefore always reports drift. Build
	@# the generator with the same canonical version string the generated Run
	@# workflow uses, so this target is reproducible locally and in CI.
	CGO_ENABLED=0 go build -trimpath \
		-ldflags '-X main.version=dev -X main.commit=unknown' \
		-o bin/nvkr-exec ./cmd/nvkr

.PHONY: workflows-check
workflows-check: workflow-generator pipeline-dev-check catalog-refresh-check ## Check committed producer, qualification, development bundles and the catalog refresh workflow for drift.
	@# Repeat the committed Plan's authored selection, including focused feature-branch
	@# Definitions. The generator still authenticates the complete bundle and inputs.
	@definition="$$(jq -er '.definitionPath | select(type == "string" and length > 0)' .github/nvkr-run/plan.json)" && \
	 environment="$$(jq -er '.environmentPath | select(type == "string" and length > 0)' .github/nvkr-run/plan.json)" && \
	 bin/nvkr-exec run generate --engine github \
		--definition "$$definition" --environment "$$environment" \
		--qualification-policy inputs/policies/nvke-qualification.yaml --root . --check
	bin/nvkr-exec run generate --engine github \
		--definition inputs/definitions/demo/arm64-nvidia-kvm.json \
		--environment inputs/environments/github-nightly-nvidia.json \
		--root . --runtime --check
	bin/nvkr-exec run generate --engine github \
		--definition inputs/definitions/demo/arm64-upstream-kvm.json \
		--environment inputs/environments/github-nightly-upstream.json \
		--root . --runtime --check

# The private NVKE source the installed catalog refresh workflow was rendered with.
CATALOG_REFRESH_SOURCE ?= dsx-ai-factory/nvke:NKE_SOURCE_TOKEN

.PHONY: catalog-refresh-check
catalog-refresh-check: workflow-generator ## Check the installed catalog refresh workflow against `catalog update-ci`.
	@# The installed file is generated, so it must equal the renderer's output for the
	@# same source option and the canonical generator stamp. Regenerate it with
	@# `make catalog-refresh-generate`; never edit the file by hand.
	@rendered="$$(mktemp)" && trap 'rm -f "$$rendered"' EXIT && \
	 bin/nvkr-exec catalog update-ci --source-checkout "$(CATALOG_REFRESH_SOURCE)" > "$$rendered" && \
	 { diff -u .github/workflows/catalog-refresh.yaml "$$rendered" || { \
	   echo "catalog-refresh.yaml differs from catalog update-ci output; regenerate it from the generator" >&2; exit 1; }; }

.PHONY: catalog-refresh-generate
catalog-refresh-generate: workflow-generator ## Regenerate the installed catalog refresh workflow with the canonical generator stamp.
	@# Render to a temporary file and replace the installed workflow only after the renderer
	@# succeeds: a refused render (source, stamp or closure admission) leaves it untouched.
	@rendered="$$(mktemp .github/workflows/.catalog-refresh.XXXXXX)" && trap 'rm -f "$$rendered"' EXIT && \
	 bin/nvkr-exec catalog update-ci --source-checkout "$(CATALOG_REFRESH_SOURCE)" > "$$rendered" && \
	 chmod 0644 "$$rendered" && mv "$$rendered" .github/workflows/catalog-refresh.yaml

.PHONY: workflows-generate
workflows-generate: workflow-generator pipeline-dev-generate ## Regenerate producer, qualification and development bundles with their canonical generator stamp.
	@definition="$$(jq -er '.definitionPath | select(type == "string" and length > 0)' .github/nvkr-run/plan.json)" && \
	 environment="$$(jq -er '.environmentPath | select(type == "string" and length > 0)' .github/nvkr-run/plan.json)" && \
	 bin/nvkr-exec run generate --engine github \
		--definition "$$definition" --environment "$$environment" \
		--qualification-policy inputs/policies/nvke-qualification.yaml \
		--root .
	bin/nvkr-exec run generate --engine github \
		--definition inputs/definitions/demo/arm64-nvidia-kvm.json \
		--environment inputs/environments/github-nightly-nvidia.json --root .
	bin/nvkr-exec run generate --engine github \
		--definition inputs/definitions/demo/arm64-upstream-kvm.json \
		--environment inputs/environments/github-nightly-upstream.json --root .

.PHONY: pipeline-dev-generate
pipeline-dev-generate: workflow-generator ## Regenerate the full feature-branch development pipeline.
	bin/nvkr-exec run generate --engine github \
		--definition inputs/definitions/nvke/definition.yaml \
		--environment inputs/environments/github-development.yaml \
		--qualification-policy inputs/policies/nvke-development-qualification.yaml --root .

.PHONY: pipeline-dev-check
pipeline-dev-check: workflow-generator ## Check development workflow generation without changing files.
	bin/nvkr-exec run generate --engine github \
		--definition inputs/definitions/nvke/definition.yaml \
		--environment inputs/environments/github-development.yaml \
		--qualification-policy inputs/policies/nvke-development-qualification.yaml --root . --check

.PHONY: pipeline-dev-run
pipeline-dev-run: pipeline-dev-check ## Dispatch the clean, pushed current branch (PIPELINE_DEV_REPO=owner/repo).
	python3 scripts/pipeline-dev.py --repo "$$PIPELINE_DEV_REPO"

.PHONY: check
check: go-version-check fmt-check license-header-check vet lint shellcheck test-ci-tools test vulncheck ## Full local gate — run before pushing.

# --- housekeeping -----------------------------------------------------------

.PHONY: clean
clean: ## Remove build output, coverage, and the local engine workspace.
	go clean
	rm -rf bin coverage.out .nvkr/work

.PHONY: run-resolve
run-resolve: build ## Resolve the pinned kubectl producer and print its build graph.
	$(BIN) resolve --data testdata/catalogs/kubernetes-layered --component kubectl --platform linux/amd64
