// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func TestReleaseVerifyAuthenticityHelpStatesPayloadLimit(t *testing.T) {
	var out bytes.Buffer
	app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	err := app.RunContext(t.Context(), []string{"nvkr", "release", "verify", "--help"})
	if err != nil || !strings.Contains(out.String(), "authenticity") ||
		!strings.Contains(out.String(), "payload integrity not checked") {
		t.Fatalf("authenticity help omitted its guarantee: err=%v output=%q", err, out.String())
	}
}

func TestReleaseVerifyAuthenticityUsesNarrowOperationAndResult(t *testing.T) {
	for _, minimum := range []lifecycle.DiskEvidenceRequirement{"", lifecycle.DiskEvidenceBuildProvenance, lifecycle.DiskEvidenceStrict} {
		t.Run(string(minimum), func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "metadata")
			called := false
			authenticate := func(ctx context.Context, reference string, policy lifecycle.VerificationPolicy,
				options lifecycle.ConsumerOptions,
			) (lifecycle.AuthenticityDescription, error) {
				called = true
				if ctx == nil || reference != authenticityCLIReference || policy.Lane != lifecycle.LanePipeline ||
					policy.Scope != lifecycle.ScopeAuthenticity || policy.IdentityRegexp != "^builder$" ||
					policy.Issuer != "issuer" || policy.MinimumDiskEvidence != minimum {
					t.Fatalf("authentication inputs not forwarded: reference=%q policy=%+v", reference, policy)
				}
				if options.WorkspaceRoot != workspace || options.CosignBinary != "pinned-cosign" ||
					options.CosignDigest != "sha256:"+strings.Repeat("b", 64) ||
					options.Registry != (delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}) {
					t.Fatalf("authentication options not forwarded: %+v", options)
				}
				return lifecycle.AuthenticityDescription{
					SchemaVersion: "nvkr.dev/release-authenticity/v1", Subject: reference,
					Lane: policy.Lane, Scope: policy.Scope, Artifacts: []lifecycle.Artifact{},
					ContentSubject: "sha256:" + strings.Repeat("c", 64), PolicyDigest: "sha256:" + strings.Repeat("d", 64),
					PayloadIntegrityVerified: false,
				}, nil
			}
			args := append(authenticityCLIArgs(workspace), "--anonymous", "--plain-http",
				"--cosign", "pinned-cosign", "--cosign-digest", "sha256:"+strings.Repeat("b", 64))
			if minimum != "" {
				args = append(args, "--minimum-disk-evidence", string(minimum))
			}
			app := &cli.App{Commands: []*cli.Command{cmdReleaseWithOperations(rejectFullVerification(t), authenticate)}}
			raw, err := runCLIWithCapturedStdout(t, app, args...)
			if err != nil || !called {
				t.Fatalf("authenticity scope did not reach authentication: called=%v error=%v", called, err)
			}
			var result lifecycle.AuthenticityDescription
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.SchemaVersion != "nvkr.dev/release-authenticity/v1" || result.Subject != authenticityCLIReference ||
				result.Scope != lifecycle.ScopeAuthenticity || result.PayloadIntegrityVerified ||
				!bytes.Contains(raw, []byte(`"payloadIntegrityVerified":false`)) || bytes.Contains(raw, []byte(`"exactInputs"`)) {
				t.Fatalf("authenticity output lost its narrow guarantee: %s", raw)
			}
		})
	}
}

func TestReleaseVerifyAuthenticityRejectsExactInputsBeforeWork(t *testing.T) {
	for _, flags := range [][]string{
		{"--definition", "missing-definition.yaml"},
		{"--environment", "missing-environment.yaml"},
		{"--plan", "missing-plan.json"},
		{"--definition", ""},
		{"--export-layout", "missing-layout"},
		{"--expected-invocation", "invocation"},
		{"--export-layout", "missing-layout", "--expected-invocation", "invocation"},
		{"--definition", "missing-definition.yaml", "--environment", "missing-environment.yaml", "--plan", "missing-plan.json"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "must-not-exist")
			err := assertAuthenticityCLIRejectedBeforeWork(t, append(authenticityCLIArgs(workspace), flags...))
			if cliErrorCode(err) != e.CodeEngineCapability {
				t.Fatalf("exact inputs did not fail capability admission: %v", err)
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected exact inputs touched workspace: %v", err)
			}
		})
	}
}

func TestReleaseVerifyAuthenticityRejectsInvalidPolicyBeforeWork(t *testing.T) {
	for _, flags := range [][]string{
		{"--identity", ""},
		{"--identity", "["},
		{"--issuer", ""},
		{"--lane", "qualified"},
		{"--minimum-disk-evidence", "unknown"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "must-not-exist")
			err := assertAuthenticityCLIRejectedBeforeWork(t, append(authenticityCLIArgs(workspace), flags...))
			if cliErrorCode(err) != e.CodeTrustPolicyMissing {
				t.Fatalf("invalid policy did not fail policy admission: %v", err)
			}
		})
	}
}

func TestReleaseVerifyAuthenticityFailureDoesNotEmitResult(t *testing.T) {
	authenticate := func(context.Context, string, lifecycle.VerificationPolicy, lifecycle.ConsumerOptions) (lifecycle.AuthenticityDescription, error) {
		return lifecycle.AuthenticityDescription{}, e.New(e.CodeSignatureInvalid, "controlled signature failure")
	}
	app := &cli.App{Commands: []*cli.Command{cmdReleaseWithOperations(rejectFullVerification(t), authenticate)}}
	raw, err := runCLIWithCapturedStdout(t, app, authenticityCLIArgs(filepath.Join(t.TempDir(), "metadata"))...)
	if cliErrorCode(err) != e.CodeSignatureInvalid || len(raw) != 0 {
		t.Fatalf("authentication failure emitted a result or changed code: error=%v output=%s", err, raw)
	}
}

const authenticityCLIReference = "registry.example.test/releases@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func authenticityCLIArgs(workspace string) []string {
	return []string{"nvkr", "release", "verify", "--reference", authenticityCLIReference,
		"--lane", "pipeline-attested", "--scope", "authenticity", "--identity", "^builder$", "--issuer", "issuer", "--workspace", workspace}
}

func rejectFullVerification(t *testing.T) releaseVerifyOperation {
	t.Helper()
	return func(context.Context, string, lifecycle.VerificationPolicy, lifecycle.ConsumerOptions, *lifecycle.ExactInputExpectation, releaseExportOptions) (releaseVerificationResult, error) {
		t.Fatal("authenticity scope invoked full payload verification")
		return releaseVerificationResult{}, nil
	}
}

func assertAuthenticityCLIRejectedBeforeWork(t *testing.T, args []string) error {
	t.Helper()
	authenticate := func(context.Context, string, lifecycle.VerificationPolicy, lifecycle.ConsumerOptions) (lifecycle.AuthenticityDescription, error) {
		t.Fatal("invalid authenticity options reached authentication")
		return lifecycle.AuthenticityDescription{}, nil
	}
	app := &cli.App{Commands: []*cli.Command{cmdReleaseWithOperations(rejectFullVerification(t), authenticate)}}
	raw, err := runCLIWithCapturedStdout(t, app, args...)
	if len(raw) != 0 {
		t.Fatalf("invalid authenticity options emitted output: %s", raw)
	}
	return err
}
