// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

var (
	fixtureSubject     = "sha256:" + strings.Repeat("a", 64)
	fixtureBuildDigest = "sha256:" + strings.Repeat("b", 64)
	fixtureImage       = "golang@sha256:" + strings.Repeat("c", 64)
)

// fakeRun stands in for a captured completed Run; lifecycle owns the real capture checks.
type fakeRun struct {
	description    lifecycle.LocalDiagnosticDescription
	descriptionErr error
	producers      map[string]*factory.CLIArtifactTestInputs
	producerErr    error
	closes         int
}

func (r *fakeRun) Description() (lifecycle.LocalDiagnosticDescription, error) {
	return r.description, r.descriptionErr
}
func (r *fakeRun) Producer(output string) (*factory.CLIArtifactTestInputs, error) {
	if r.producerErr != nil {
		return nil, r.producerErr
	}
	inputs, ok := r.producers[output]
	if !ok {
		return nil, e.New(e.CodeUsage, "no matching standalone CLI output")
	}
	copied := *inputs
	return &copied, nil
}
func (r *fakeRun) Close() error { r.closes++; return nil }

func fixtureOutput() delivery.PublicationOutput {
	return delivery.PublicationOutput{
		Output:        delivery.Output{Name: "nvkectl", Kind: lifecycle.OutputExecutable, OS: "linux", Arch: "arm64", BuildDigest: fixtureBuildDigest, Components: []string{"nvkectl"}},
		BuildSubjects: []delivery.BuildSubject{{Component: "nvkectl", Digest: fixtureSubject}},
	}
}

func fixtureRun() *fakeRun {
	return &fakeRun{
		description: lifecycle.LocalDiagnosticDescription{Attempt: "build-001", PlanDigest: "sha256:" + strings.Repeat("d", 64),
			Outputs: []delivery.PublicationOutput{fixtureOutput()}},
		producers: map[string]*factory.CLIArtifactTestInputs{"nvkectl": {
			Output: "nvkectl", BuildDigest: fixtureBuildDigest, Platform: "linux/arm64",
			Repo: "https://github.com/dsx-ai-factory/nvke", Commit: strings.Repeat("1", 40), TreeHash: strings.Repeat("2", 40),
			ToolchainImage: fixtureImage, ExpectedVersion: "v1.2.3",
		}},
	}
}

func preparedFixture(t *testing.T) *PreparedValidation {
	t.Helper()
	p, err := Prepare(t.Context(), registrationFixture(t), []string{"nvke/nvkectl/contract", "nvke/nvkectl/cli"}, "local")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func opener(run *fakeRun, opened *[]string) func(context.Context, string) (completedRun, error) {
	return func(_ context.Context, attempt string) (completedRun, error) {
		*opened = append(*opened, attempt)
		return run, nil
	}
}

func TestBindExistingRun(t *testing.T) {
	run, opened := fixtureRun(), []string{}
	bound, err := preparedFixture(t).bind(t.Context(), ".nvkr/runs/build-001", opener(run, &opened))
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != ".nvkr/runs/build-001" {
		t.Fatal(opened)
	}
	if identity := bound.Run(); identity.Attempt != "build-001" || identity.PlanDigest != run.description.PlanDigest {
		t.Fatalf("run identity: %+v", identity)
	}
	suites := bound.Suites()
	if len(suites) != 2 || suites[0].ID != "nvke/nvkectl/contract" || suites[1].ID != "nvke/nvkectl/cli" {
		t.Fatalf("bound suites changed order: %+v", suites)
	}
	for _, suite := range suites {
		input := suite.Inputs["executable"]
		if input != (BoundInput{Output: "nvkectl", Component: "nvkectl", Format: "executable", Platform: "linux/arm64",
			Digest: fixtureSubject, BuildDigest: fixtureBuildDigest, Version: "v1.2.3"}) {
			t.Fatalf("unexpected binding: %+v", input)
		}
		if suite.TestOrigin != (TestOrigin{Repository: "https://github.com/dsx-ai-factory/nvke", Commit: strings.Repeat("1", 40), Tree: strings.Repeat("2", 40)}) ||
			suite.Image != fixtureImage || suite.DocumentSHA256 == "" {
			t.Fatalf("unexpected resolution: %+v", suite)
		}
	}
	suites[0].Inputs["executable"] = BoundInput{Output: "poison"}
	if bound.Suites()[0].Inputs["executable"].Output != "nvkectl" {
		t.Fatal("caller mutated bound validation")
	}
	if err := bound.Close(); err != nil || run.closes != 1 {
		t.Fatalf("close: %v, %d", err, run.closes)
	}
	if err := bound.Close(); err != nil || run.closes != 1 {
		t.Fatal("close is not idempotent")
	}
}

func TestBindRefusesIncompatibleRuns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*fakeRun)
		code   e.Code
	}{
		{"missing-output", func(r *fakeRun) { r.description.Outputs[0].Name = "other" }, e.CodeVerifyFailed},
		{"ambiguous-output", func(r *fakeRun) { r.description.Outputs = append(r.description.Outputs, fixtureOutput()) }, e.CodeVerifyFailed},
		{"wrong-format", func(r *fakeRun) { r.description.Outputs[0].Kind = lifecycle.OutputOCI }, e.CodeEngineCapability},
		{"excluded-platform", func(r *fakeRun) { r.description.Outputs[0].OS = "darwin" }, e.CodeEngineCapability},
		{"missing-component", func(r *fakeRun) { r.description.Outputs[0].BuildSubjects[0].Component = "kubectl" }, e.CodeVerifyFailed},
		{"ambiguous-component", func(r *fakeRun) {
			r.description.Outputs[0].BuildSubjects = append(r.description.Outputs[0].BuildSubjects, delivery.BuildSubject{Component: "nvkectl", Digest: fixtureBuildDigest})
		}, e.CodeVerifyFailed},
		{"producer-refused", func(r *fakeRun) { r.producerErr = e.New(e.CodeInvalidCatalog, "CLI version stamp is required") }, e.CodeInvalidCatalog},
		{"producer-build-differs", func(r *fakeRun) { r.producers["nvkectl"].BuildDigest = fixtureSubject }, e.CodeVerifyFailed},
		{"producer-platform-differs", func(r *fakeRun) { r.producers["nvkectl"].Platform = "linux/amd64" }, e.CodeVerifyFailed},
		{"description-refused", func(r *fakeRun) { r.descriptionErr = e.New(e.CodeGateUnmet, "local diagnostic artifacts are closed") }, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, opened := fixtureRun(), []string{}
			tc.mutate(run)
			bound, err := preparedFixture(t).bind(t.Context(), ".nvkr/runs/build-001", opener(run, &opened))
			catalogCode(t, err, tc.code)
			if bound != nil || run.closes != 1 {
				t.Fatalf("refusal kept the Run open: bound=%v closes=%d", bound != nil, run.closes)
			}
		})
	}
}

func TestBindAdmissionBeforeOpeningRun(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name     string
		prepared *PreparedValidation
		ctx      context.Context
		run      string
		code     e.Code
	}{
		{"nil-prepared", nil, t.Context(), ".nvkr/runs/build-001", e.CodeUsage},
		{"nil-context", preparedFixture(t), nil, ".nvkr/runs/build-001", e.CodeUsage},
		{"canceled", preparedFixture(t), canceled, ".nvkr/runs/build-001", e.CodeTimeout},
		{"empty-run", preparedFixture(t), t.Context(), "", e.CodeUsage},
		{"unsafe-run", preparedFixture(t), t.Context(), " .nvkr/runs/build-001", e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, opened := fixtureRun(), []string{}
			bound, err := tc.prepared.bind(tc.ctx, tc.run, opener(run, &opened))
			catalogCode(t, err, tc.code)
			if bound != nil || len(opened) != 0 {
				t.Fatal("admission refusal opened the Run")
			}
		})
	}
	// An open failure is returned unchanged with nothing to close.
	failed := func(context.Context, string) (completedRun, error) {
		return nil, e.New(e.CodeGateUnmet, "local qualification requires a complete local Run")
	}
	_, err := preparedFixture(t).bind(t.Context(), ".nvkr/runs/build-001", failed)
	catalogCode(t, err, e.CodeGateUnmet)
}

// The production opener reaches the lifecycle owner, which refuses a missing Run.
func TestBindExistingRunUsesLifecycleCapture(t *testing.T) {
	prepared := preparedFixture(t)
	t.Chdir(t.TempDir())
	bound, err := prepared.Bind(t.Context(), ".nvkr/runs/missing")
	if err == nil || bound != nil {
		t.Fatal("missing Run was bound")
	}
	var coded *e.Error
	if !errors.As(err, &coded) {
		t.Fatalf("uncoded refusal: %v", err)
	}
}
