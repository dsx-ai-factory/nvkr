// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestNativeSmokeFailureDiagnosticsCoverKnownMembers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     e.Code
		statuses []string
		observed int
	}{
		{"runtime admission", e.CodeEngineCapability, []string{"not-evaluated", "not-evaluated"}, 0},
		{"first execution", e.CodeBuildFailed, []string{"fail", "not-evaluated"}, 0},
		{"second execution", e.CodeBuildFailed, []string{"pass", "fail"}, 1},
		{"version mismatch", e.CodeVerifyFailed, []string{"fail", "not-evaluated"}, 1},
		{"between members revoked", e.CodeProvenanceMissing, []string{"pass", "not-evaluated"}, 1},
		{"runtime cleanup", e.CodeEngineCapability, []string{"pass", "pass"}, 2},
		{"final cancellation", e.CodeTimeout, []string{"pass", "pass"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks, invocation, policy, owner := nativeSmokeLiveFixture(t)
			if len(owner.expected) != len(tc.statuses) {
				t.Fatal("fixture must contain two independently named outputs")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cause := errors.New("diagnostic fixture failure")
			original := e.Wrap(tc.code, cause, "native smoke refused")
			switch tc.name {
			case "first execution", "second execution":
				failAt := 0
				if tc.name == "second execution" {
					failAt = 1
				}
				owner.runHook = func(context.Context) error {
					if owner.count == failAt {
						return original
					}
					return nil
				}
			case "version mismatch":
				owner.expected[0].ExpectedVersion = "vwrong"
			case "between members revoked":
				owner.runHook = func(context.Context) error { invocation.closed.Store(true); return nil }
			case "runtime cleanup":
				owner.closeHook = func() error { return cause }
			case "final cancellation":
				owner.closeHook = func() error { cancel(); return nil }
			}
			receipt, report, err := runNativeToolSmoke(ctx, checks, invocation, policy, NativeToolSmokeOptions{}, "linux/amd64", func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) {
				if tc.name == "runtime admission" {
					return nil, original
				}
				return owner, nil
			})
			assertCode(t, err, tc.code)
			if tc.name == "runtime admission" || tc.name == "first execution" || tc.name == "second execution" {
				if !errors.Is(err, original) || !errors.Is(err, cause) {
					t.Fatal("original failure cause changed")
				}
			}
			if tc.name == "runtime cleanup" && !errors.Is(err, cause) {
				t.Fatal("cleanup cause changed")
			}
			if tc.name == "final cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause changed")
			}
			if receipt != nil || report == nil || report.Status != "fail" || report.Code != string(tc.code) {
				t.Fatalf("failure issued authority or lost diagnostics: receipt=%v report=%+v", receipt, report)
			}
			if tc.name != "runtime admission" && !owner.closed {
				t.Fatal("failure returned before runtime cleanup")
			}
			if len(report.Results) != len(owner.expected) || len(report.Members) != tc.observed {
				t.Fatalf("incomplete or invented observations: %+v", report)
			}
			for i, result := range report.Results {
				member := owner.expected[i]
				if result.Output != member.Output || result.Member != member.Member || result.Status != tc.statuses[i] {
					t.Fatalf("wrong member result: %+v", result)
				}
				if result.Status == "pass" {
					if result.Code != "" || result.Reason != "" {
						t.Fatalf("observed pass changed: %+v", result)
					}
				} else if result.Code != string(tc.code) || result.Reason == "" {
					t.Fatalf("failure diagnostic has no cause: %+v", result)
				}
			}
			for _, output := range report.ArtifactChecks.Outputs {
				if output.Overall != "pass" {
					t.Fatal("smoke failure rewrote actual artifact checks")
				}
			}
			if _, err := report.Canonical(); err == nil {
				t.Fatal("failed diagnostics became signed worker transport")
			}
		})
	}
}

func TestNativeSmokeSuccessfulTransportExcludesFailureResults(t *testing.T) {
	checks, invocation, policy, owner := nativeSmokeLiveFixture(t)
	receipt, report, err := runNativeToolSmoke(t.Context(), checks, invocation, policy, NativeToolSmokeOptions{}, "linux/amd64", func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) { return owner, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	raw, err := report.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 0 || bytes.Contains(raw, []byte(`"results"`)) {
		t.Fatal("success wire bytes acquired failure-only fields")
	}
	for _, status := range []string{"pass", "fail", "not-evaluated"} {
		report.Results = []NativeToolSmokeResult{{Output: owner.expected[0].Output, Member: owner.expected[0].Member, Status: status}}
		_, err := report.Canonical()
		assertCode(t, err, e.CodeVerifyFailed)
	}
	held, err := receipt.Description()
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := held.Canonical()
	if err != nil || !bytes.Equal(raw, frozen) {
		t.Fatalf("diagnostic mutation changed held success: %v", err)
	}
}
