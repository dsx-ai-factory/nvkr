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

func TestReleaseBrowseRejectsPresentationBeforeAcquisition(t *testing.T) {
	for _, extra := range [][]string{{"--format", "html"}, {"--timeout", "0s"}, {"--layout", "missing", "--anonymous"}, {"unexpected"}} {
		var out bytes.Buffer
		app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
		args := []string{"nvkr", "release", "browse", "--reference", "127.0.0.1:1/releases@sha256:" + strings.Repeat("a", 64), "--workspace", filepath.Join(t.TempDir(), "must-not-exist")}
		err := app.RunContext(t.Context(), append(args, extra...))
		if cliErrorCode(err) != e.CodeUsage || out.Len() != 0 {
			t.Fatalf("invalid options reached acquisition or output: %v, %q", err, out.String())
		}
	}
}

func TestReleaseBrowseRejectsInvalidReferencesWithoutEffects(t *testing.T) {
	for _, reference := range []string{"", " ", "registry.example.test/releases", "registry.example.test/releases:latest", "registry.example.test/releases@sha256:bad"} {
		t.Run(reference, func(t *testing.T) {
			var out bytes.Buffer
			app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
			workspace := filepath.Join(t.TempDir(), "must-not-exist")
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "browse", "--reference", reference, "--workspace", workspace})
			if cliErrorCode(err) != e.CodeUnverifiedRef || out.Len() != 0 {
				t.Fatalf("invalid reference reached acquisition or output: %v, %q", err, out.String())
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid reference touched workspace: %v", err)
			}
		})
	}
}

func TestReleaseVerifyExposesAndValidatesDiskEvidencePolicy(t *testing.T) {
	var out bytes.Buffer
	app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	err := app.RunContext(t.Context(), []string{"nvkr", "release", "verify", "--help"})
	if err != nil || !strings.Contains(out.String(), "minimum-disk-evidence") || !strings.Contains(out.String(), "build-provenance-v1") {
		t.Fatalf("release verify help omitted disk evidence policy: err=%v output=%q", err, out.String())
	}
	if !strings.Contains(out.String(), "build-provenance-v1 for pipeline-attested; strict for qualified") {
		t.Fatalf("release verify help omitted lane-specific defaults: %q", out.String())
	}

	out.Reset()
	app = &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	err = app.RunContext(t.Context(), []string{"nvkr", "release", "verify", "--reference", "registry.example.test/product@sha256:" + strings.Repeat("a", 64),
		"--lane", "qualified", "--scope", "release", "--minimum-disk-evidence", "build-provenance-v1"})
	if cliErrorCode(err) != e.CodeTrustPolicyMissing || out.Len() != 0 {
		t.Fatalf("qualified producer-only policy was not rejected before adapters: err=%v output=%q", err, out.String())
	}

	out.Reset()
	app = &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	err = app.RunContext(t.Context(), []string{"nvkr", "release", "verify", "--reference", "registry.example.test/product@sha256:" + strings.Repeat("a", 64),
		"--lane", "qualified", "--scope", "release"})
	if cliErrorCode(err) != e.CodeEngineCapability || out.Len() != 0 {
		t.Fatalf("qualified default was weakened or reached an adapter: err=%v output=%q", err, out.String())
	}
}

func TestReleaseVerifyRequiresCompleteExactInputSetBeforeAcquisition(t *testing.T) {
	for _, flags := range [][]string{
		{"--definition", "definition.yaml"},
		{"--environment", "environment.yaml"},
		{"--plan", "plan.json"},
		{"--definition", "definition.yaml", "--environment", "environment.yaml"},
		{"--definition", "definition.yaml", "--plan", "plan.json"},
		{"--environment", "environment.yaml", "--plan", "plan.json"},
		{"--definition", "", "--environment", "", "--plan", ""},
	} {
		workspace := filepath.Join(t.TempDir(), "must-not-exist")
		args := []string{"nvkr", "release", "verify",
			"--reference", "127.0.0.1:1/releases@sha256:" + strings.Repeat("a", 64),
			"--lane", "pipeline-attested", "--scope", "artifact-publication",
			"--identity", "^builder$", "--issuer", "issuer", "--workspace", workspace,
		}
		app := &cli.App{Commands: []*cli.Command{cmdRelease()}}
		err := app.RunContext(t.Context(), append(args, flags...))
		if cliErrorCode(err) != e.CodeUsage {
			t.Fatalf("partial exact-input flags reached acquisition: flags=%v error=%v", flags, err)
		}
	}
}

func TestReleaseVerifyReportsUnsupportedExactInputPolicy(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags []string
	}{
		{name: "qualified release", flags: []string{"--lane", "qualified", "--scope", "release"}},
		{name: "pipeline release", flags: []string{"--lane", "pipeline-attested", "--scope", "release",
			"--identity", "^builder$", "--issuer", "issuer"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"nvkr", "release", "verify",
				"--reference", "registry.example.test/releases@sha256:" + strings.Repeat("a", 64),
				"--definition", "definition.yaml", "--environment", "environment.yaml", "--plan", "plan.json",
			}
			app := &cli.App{Commands: []*cli.Command{cmdRelease()}}
			err := app.RunContext(t.Context(), append(args, test.flags...))
			if cliErrorCode(err) != e.CodeEngineCapability {
				t.Fatalf("unsupported exact-input policy returned %v", err)
			}
		})
	}
}

func TestReleaseVerifyLoadsCompleteExactInputsBeforeAcquisition(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "must-not-exist")
	missing := filepath.Join(t.TempDir(), "missing-definition.yaml")
	args := []string{"nvkr", "release", "verify",
		"--reference", "registry.example.test/releases@sha256:" + strings.Repeat("a", 64),
		"--lane", "pipeline-attested", "--scope", "artifact-publication",
		"--identity", "^builder$", "--issuer", "issuer", "--workspace", workspace,
		"--definition", missing, "--environment", "environment.yaml", "--plan", "plan.json",
	}
	app := &cli.App{Commands: []*cli.Command{cmdRelease()}}
	err := app.RunContext(t.Context(), args)
	if cliErrorCode(err) != e.CodeInvalidCatalog {
		t.Fatalf("complete exact-input load returned %v", err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid exact inputs touched workspace: %v", err)
	}
}

func TestReleaseVerifyForwardsCompleteExactInputsToLifecycleUseCase(t *testing.T) {
	lifecycleCommandRepo(t)
	const reference = "registry.example.test/releases@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const policyDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const planDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	called := false
	verify := func(_ context.Context, gotReference string, policy lifecycle.VerificationPolicy,
		options lifecycle.ConsumerOptions, expectation *lifecycle.ExactInputExpectation,
	) (releaseVerificationResult, error) {
		called = true
		if gotReference != reference || policy.Lane != lifecycle.LanePipeline ||
			policy.Scope != lifecycle.ScopeArtifactPublication || expectation == nil {
			t.Fatalf("exact verification was not forwarded: reference=%q policy=%+v expectation=%v",
				gotReference, policy, expectation)
		}
		if options.Registry != (delivery.RegistryOptions{Anonymous: true}) {
			t.Fatalf("consumer options were not forwarded: %+v", options)
		}
		exact := lifecycle.ExactInputDescription{ReleaseSubject: reference, PolicyDigest: policyDigest,
			DefinitionDigest:  "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			EnvironmentDigest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			PlanDigest:        planDigest, ContentSubject: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
		return releaseVerificationResult{Release: lifecycle.Release{SchemaVersion: lifecycle.ReleaseSchemaVersion,
			Subject: reference, Lane: policy.Lane, Scope: policy.Scope, PolicyDigest: policyDigest,
			ContentSubject: exact.ContentSubject}, ExactInputs: &exact}, nil
	}
	args := []string{"nvkr", "release", "verify",
		"--reference", reference,
		"--lane", "pipeline-attested", "--scope", "artifact-publication",
		"--identity", "^builder$", "--issuer", "issuer", "--anonymous",
		"--definition", "definitions/nvke-m2/definition.yaml",
		"--environment", "definitions/nvke-m2/environment-github.yaml",
		"--plan", ".github/nvkr-run/plan.json",
	}
	app := &cli.App{Commands: []*cli.Command{cmdReleaseWithVerify(verify)}}
	raw, err := runCLIWithCapturedStdout(t, app, args...)
	if err != nil || !called {
		t.Fatalf("complete exact-input path did not reach the lifecycle use case: called=%v error=%v", called, err)
	}
	var result releaseVerificationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != releaseVerificationResultSchemaVersion ||
		result.Release.Subject != reference || result.ExactInputs == nil ||
		result.ExactInputs.ReleaseSubject != reference || result.ExactInputs.PolicyDigest != policyDigest ||
		result.ExactInputs.PlanDigest != planDigest {
		t.Fatalf("exact-input description was not serialized in the versioned result: %+v", result)
	}
}

func TestReleaseVerifyForwardsDiskEvidencePolicyWithExactInputs(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, minimum := range []lifecycle.DiskEvidenceRequirement{"", lifecycle.DiskEvidenceBuildProvenance, lifecycle.DiskEvidenceStrict} {
		t.Run(string(minimum), func(t *testing.T) {
			called := false
			verify := func(_ context.Context, _ string, policy lifecycle.VerificationPolicy,
				_ lifecycle.ConsumerOptions, expectation *lifecycle.ExactInputExpectation,
			) (releaseVerificationResult, error) {
				called = true
				if expectation == nil || policy.Lane != lifecycle.LanePipeline || policy.Scope != lifecycle.ScopeArtifactPublication || policy.MinimumDiskEvidence != minimum {
					t.Fatalf("exact-input disk policy was not forwarded together: policy=%+v expectation=%v", policy, expectation)
				}
				return releaseVerificationResult{}, e.New(e.CodeGateUnmet, "controlled verification boundary")
			}
			args := []string{"nvkr", "release", "verify",
				"--reference", "registry.example.test/releases@sha256:" + strings.Repeat("a", 64),
				"--lane", "pipeline-attested", "--scope", "artifact-publication",
				"--identity", "^builder$", "--issuer", "issuer",
				"--definition", "definitions/nvke-m2/definition.yaml",
				"--environment", "definitions/nvke-m2/environment-github.yaml",
				"--plan", ".github/nvkr-run/plan.json",
			}
			if minimum != "" {
				args = append(args, "--minimum-disk-evidence", string(minimum))
			}
			app := &cli.App{Commands: []*cli.Command{cmdReleaseWithVerify(verify)}}
			err := app.RunContext(t.Context(), args)
			if !called || cliErrorCode(err) != e.CodeGateUnmet {
				t.Fatalf("combined policy did not reach the lifecycle boundary: called=%v error=%v", called, err)
			}
		})
	}
}
