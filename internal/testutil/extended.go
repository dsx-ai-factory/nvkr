// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package testutil contains shared test-suite controls.
package testutil

import (
	"os"
	"testing"
)

// RequireExtended keeps capacity and full-product integration fixtures out of
// ordinary go test invocations, including agent runs. Short mode always wins.
func RequireExtended(t *testing.T) {
	t.Helper()
	if testing.Short() || os.Getenv("NKD_TEST_EXTENDED") != "1" {
		t.Skip("extended capacity/integration test; run make test-full or NKD_TEST_EXTENDED=1 go test")
	}
}
