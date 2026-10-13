// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

func TestTerminalDiagnosticCannotInjectDisplayControls(t *testing.T) {
	for _, message := range []string{"registry: \x1b[2Jfalse success", "registry: \x1b]8;;https://untrusted.example\x07link", "registry: failed\nfalse success", "registry: \u202econcealed", "registry: \x9binvalid UTF-8"} {
		t.Run(message, func(t *testing.T) {
			got := terminalDiagnostic(message)
			if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) || strings.ContainsAny(got, "\x1b\x07\n\x9b\u202e") {
				t.Fatalf("unsafe diagnostic %q", got)
			}
		})
	}
	ordinary := `[NVKR-E-VERIFY-FAILED] registry "example.com" rejected résumé`
	if got := terminalDiagnostic(ordinary); got != ordinary {
		t.Fatalf("ordinary error changed: %q", got)
	}
}
