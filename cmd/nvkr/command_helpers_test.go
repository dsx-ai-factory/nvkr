// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func verifyTestDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func cliErrorCode(err error) nvkrerr.Code {
	var coded *nvkrerr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCanonicalInputRejectsCancellationAndFIFOs(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var decoded any
	if err := readCanonicalJSONContext(ctx, missing, &decoded); cliErrorCode(err) != nvkrerr.CodeTimeout || !nvkrerr.Retryable(err) {
		t.Fatalf("canceled canonical input: %v", err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readCanonicalJSONContext(context.Background(), fifo, &decoded); cliErrorCode(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("FIFO canonical input: %v", err)
	}
}
