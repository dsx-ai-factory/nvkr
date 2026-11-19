# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# This module temporarily imports core NVKR; it has its own check scope.
GOTOOLCHAIN ?= local
export GOTOOLCHAIN
GO_TEST_FLAGS ?=
GO_TEST_TIMEOUT ?= 20m
BIN_DIR ?= ../../bin
GOSRC := cmd internal pkg
.DEFAULT_GOAL := build

.PHONY: build test test-deps context-roots vet lint fmt-check vulncheck tidy check
build:
	go build -o $(BIN_DIR)/nvke-demo ./cmd/nvke-demo
	go build -o $(BIN_DIR)/nvkr-site-identity-helper ./cmd/nvkr-site-identity-helper

test: test-deps
	go test -count=1 -timeout $(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS) ./...

# Boundary tests run a fresh external consumer and inspect the root CLI offline.
# Their module graph needs more than the packages compiled by this module alone.
test-deps:
	go -C ../.. mod download
	go mod download

context-roots:
	go -C ../.. test ./internal/architecture -run '^TestDemoContextRootsMatchExactInventory$$' -count=1

vet:
	go vet ./...

lint:
	golangci-lint run --config ../../.golangci.yml

fmt-check:
	@unformatted="$$(gofmt -l $(GOSRC))" || exit $$?; \
	 test -z "$$unformatted" || { printf '%s\n' "$$unformatted"; exit 1; }

vulncheck:
	GOBIN="$(abspath ../../bin)" go -C ../../toolchains/govulncheck install tool
	../../bin/govulncheck ./...

tidy:
	gofmt -s -w $(GOSRC)
	go mod tidy

check: fmt-check context-roots vet build test lint vulncheck
