// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package errors

import (
	stderrors "errors"
	"fmt"
	"testing"
)

// TestExitCodeContract is the snapshot/invariant guard ADR-03 §3 requires: every
// declared Code maps to exactly one exit status, all statuses are pairwise
// distinct, the allocation is banded, and the POSIX/signal ranges are avoided. A
// reorder, collision, or stray code fails the build.
func TestExitCodeContract(t *testing.T) {
	// Every AllCodes entry has an exit mapping, and vice versa (no orphans in
	// either direction).
	if len(AllCodes) != len(exitCodes) {
		t.Fatalf("AllCodes has %d entries but exitCodes has %d", len(AllCodes), len(exitCodes))
	}
	seenCode := map[Code]bool{}
	for _, c := range AllCodes {
		if seenCode[c] {
			t.Errorf("Code %s appears twice in AllCodes", c)
		}
		seenCode[c] = true
		if _, ok := exitCodes[c]; !ok {
			t.Errorf("Code %s in AllCodes has no exitCodes mapping", c)
		}
	}
	for c := range exitCodes {
		if !seenCode[c] {
			t.Errorf("Code %s in exitCodes is missing from AllCodes", c)
		}
	}

	// Exit statuses are pairwise-distinct and avoid the reserved ranges.
	byStatus := map[int]Code{}
	for c, status := range exitCodes {
		if prev, dup := byStatus[status]; dup {
			t.Errorf("exit status %d used by both %s and %s", status, prev, c)
		}
		byStatus[status] = c
		if status == 0 {
			t.Errorf("Code %s maps to 0, which is reserved for success", c)
		}
		if status >= 126 && status <= 165 {
			t.Errorf("Code %s maps to %d, inside the reserved POSIX/signal range 126-165", c, status)
		}
	}

	// Banding: each code sits in the band ADR-03 assigns it.
	band := func(status int) string {
		switch {
		case status >= 0 && status <= 3:
			return "semantic"
		case status >= 10 && status <= 19:
			return "resolve/input/preflight/crypto"
		case status >= 20 && status <= 29:
			return "build/verify/trust/disk"
		case status >= 30 && status <= 31:
			return "retryable"
		case status >= 40 && status <= 54:
			return "release/lifecycle"
		default:
			return "OUT-OF-BAND"
		}
	}
	wantBand := map[Code]string{
		CodeInternal: "semantic", CodeUsage: "semantic", CodeDiff: "semantic",

		CodeNoMatch: "resolve/input/preflight/crypto", CodeAmbiguous: "resolve/input/preflight/crypto",
		CodeReservedAxis: "resolve/input/preflight/crypto", CodeUnsupported: "resolve/input/preflight/crypto",
		CodeUnpinnedSource: "resolve/input/preflight/crypto", CodeTagToolchain: "resolve/input/preflight/crypto",
		CodeInvalidCatalog: "resolve/input/preflight/crypto", CodePreflightFailed: "resolve/input/preflight/crypto",
		CodeBuilderNotProvided: "resolve/input/preflight/crypto", CodeSignatureInvalid: "resolve/input/preflight/crypto",

		CodeFetchFailed: "build/verify/trust/disk", CodeBuildFailed: "build/verify/trust/disk",
		CodeVerifyFailed:    "build/verify/trust/disk",
		CodeUntrustedSigner: "build/verify/trust/disk", CodeTrustPolicyMissing: "build/verify/trust/disk",
		CodeUntrustedProfile: "build/verify/trust/disk", CodeEncryptedArtifact: "build/verify/trust/disk",
		CodeEnvelopeDecode: "build/verify/trust/disk", CodeEngineCapability: "build/verify/trust/disk",

		CodeUnavailable: "retryable", CodeTimeout: "retryable",

		CodeVectorUnpinned: "release/lifecycle", CodeReleaseIncomplete: "release/lifecycle",
		CodeGateUnmet: "release/lifecycle", CodeUnverifiedRef: "release/lifecycle",
		CodeCompatMismatch: "release/lifecycle", CodeProvenanceMissing: "release/lifecycle",
		CodeInventoryUnpinned: "release/lifecycle", CodeImmutableViolation: "release/lifecycle",
		CodeWithdrawn: "release/lifecycle", CodeUnpinnedInput: "release/lifecycle",
		CodeMirrorMismatch: "release/lifecycle", CodePipelineDrift: "release/lifecycle",
		CodePinSetMismatch: "release/lifecycle", CodeStatusCheckpoint: "release/lifecycle", CodeWriteConflict: "release/lifecycle",
	}
	for c, status := range exitCodes {
		got := band(status)
		if got == "OUT-OF-BAND" {
			t.Errorf("Code %s maps to %d, which is outside every defined band", c, status)
		}
		if want, ok := wantBand[c]; ok && got != want {
			t.Errorf("Code %s (exit %d) is in band %q, want %q", c, status, got, want)
		}
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"coded", New(CodeNoMatch, "x"), 10},
		{"wrapped-coded", fmt.Errorf("outer: %w", New(CodeVerifyFailed, "y")), 22},
		{"plain", stderrors.New("boom"), 1},
		{"unmapped-code", &Error{Code: "NVKR-E-NOPE", Msg: "z"}, 1},
	}
	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("%s: ExitCode = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unavailable", New(CodeUnavailable, "5xx"), true},
		{"timeout", New(CodeTimeout, "deadline"), true},
		{"wrapped-timeout", fmt.Errorf("outer: %w", New(CodeTimeout, "deadline")), true},
		{"build-failed", New(CodeBuildFailed, "nonzero"), false},
		{"plain", stderrors.New("boom"), false},
	}
	for _, tc := range cases {
		if got := Retryable(tc.err); got != tc.want {
			t.Errorf("%s: Retryable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
