// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// fakeBuild stands in for a compiled, not yet executed local Run.
type fakeBuild struct {
	producers  map[string]*factory.CLIArtifactTestInputs
	executeErr error
	attempt    string
	executed   int
}

func (b *fakeBuild) Producer(output string) (*factory.CLIArtifactTestInputs, error) {
	inputs, ok := b.producers[output]
	if !ok {
		return nil, e.New(e.CodeUsage, "no matching standalone CLI output")
	}
	copied := *inputs
	return &copied, nil
}

func (b *fakeBuild) Execute(context.Context) (string, error) {
	b.executed++
	if b.executeErr != nil {
		return "", b.executeErr
	}
	return b.attempt, nil
}

func fixtureBuild() *fakeBuild {
	return &fakeBuild{producers: fixtureRun().producers, attempt: ".nvkr/runs/build-001"}
}

func fixtureBuildRequest() BuildRequest {
	return BuildRequest{Definition: "inputs/definitions/example.yaml", Environment: "inputs/environments/local.yaml",
		Attempt: "build-001", GeneratorVersion: "test"}
}

func planner(build *fakeBuild, planned *[]BuildRequest) func(context.Context, BuildRequest) (plannedBuild, error) {
	return func(_ context.Context, request BuildRequest) (plannedBuild, error) {
		*planned = append(*planned, request)
		return build, nil
	}
}

func TestBuildThenBindNewRun(t *testing.T) {
	build, run, planned, opened := fixtureBuild(), fixtureRun(), []BuildRequest{}, []string{}
	request := fixtureBuildRequest()
	bound, err := preparedFixture(t).build(t.Context(), request, planner(build, &planned), opener(run, &opened))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bound.Close() })
	if len(planned) != 1 || planned[0] != request || build.executed != 1 {
		t.Fatalf("planned %v, executed %d", planned, build.executed)
	}
	// The new attempt is bound through the same path as an existing Run.
	if len(opened) != 1 || opened[0] != ".nvkr/runs/build-001" {
		t.Fatal(opened)
	}
	suites := bound.Suites()
	if len(suites) != 2 || suites[0].Inputs["executable"].Digest != fixtureSubject || suites[1].Image != fixtureImage {
		t.Fatalf("unexpected binding: %+v", suites)
	}
}

func TestBuildRefusedBeforeBuilding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*fakeBuild)
		code   e.Code
	}{
		{"output-not-in-plan", func(b *fakeBuild) { delete(b.producers, "nvkectl") }, e.CodeUsage},
		{"platform-not-declared", func(b *fakeBuild) { b.producers["nvkectl"].Platform = "linux/riscv64" }, e.CodeEngineCapability},
		{"producer-output-differs", func(b *fakeBuild) { b.producers["nvkectl"].Output = "other" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build, run, planned, opened := fixtureBuild(), fixtureRun(), []BuildRequest{}, []string{}
			tc.mutate(build)
			bound, err := preparedFixture(t).build(t.Context(), fixtureBuildRequest(), planner(build, &planned), opener(run, &opened))
			catalogCode(t, err, tc.code)
			if bound != nil || build.executed != 0 || len(opened) != 0 {
				t.Fatalf("refusal built or bound: executed=%d opened=%v", build.executed, opened)
			}
		})
	}
	// A compilation failure is returned unchanged; nothing is built.
	failed := func(context.Context, BuildRequest) (plannedBuild, error) {
		return nil, e.New(e.CodeInvalidCatalog, "invalid Definition")
	}
	run, opened := fixtureRun(), []string{}
	_, err := preparedFixture(t).build(t.Context(), fixtureBuildRequest(), failed, opener(run, &opened))
	catalogCode(t, err, e.CodeInvalidCatalog)
	if len(opened) != 0 {
		t.Fatal("compilation failure opened a Run")
	}
}

func TestBuildFailureStartsNoBinding(t *testing.T) {
	build, run, planned, opened := fixtureBuild(), fixtureRun(), []BuildRequest{}, []string{}
	build.executeErr = e.New(e.CodeBuildFailed, "producer failed")
	bound, err := preparedFixture(t).build(t.Context(), fixtureBuildRequest(), planner(build, &planned), opener(run, &opened))
	catalogCode(t, err, e.CodeBuildFailed)
	if bound != nil || build.executed != 1 || len(opened) != 0 {
		t.Fatalf("failed build was bound: executed=%d opened=%v", build.executed, opened)
	}
}

func TestBuildAdmissionBeforeCompiling(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	with := func(change func(*BuildRequest)) BuildRequest {
		request := fixtureBuildRequest()
		change(&request)
		return request
	}
	for _, tc := range []struct {
		name     string
		prepared *PreparedValidation
		ctx      context.Context
		request  BuildRequest
		code     e.Code
	}{
		{"nil-prepared", nil, t.Context(), fixtureBuildRequest(), e.CodeUsage},
		{"nil-context", preparedFixture(t), nil, fixtureBuildRequest(), e.CodeUsage},
		{"canceled", preparedFixture(t), canceled, fixtureBuildRequest(), e.CodeTimeout},
		{"no-definition", preparedFixture(t), t.Context(), with(func(r *BuildRequest) { r.Definition = "" }), e.CodeUsage},
		{"no-environment", preparedFixture(t), t.Context(), with(func(r *BuildRequest) { r.Environment = "" }), e.CodeUsage},
		{"no-attempt", preparedFixture(t), t.Context(), with(func(r *BuildRequest) { r.Attempt = "" }), e.CodeUsage},
		{"no-generator-version", preparedFixture(t), t.Context(), with(func(r *BuildRequest) { r.GeneratorVersion = "" }), e.CodeUsage},
		{"unsafe-attempts-root", preparedFixture(t), t.Context(), with(func(r *BuildRequest) { r.AttemptsRoot = " .nvkr/runs" }), e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build, run, planned, opened := fixtureBuild(), fixtureRun(), []BuildRequest{}, []string{}
			bound, err := tc.prepared.build(tc.ctx, tc.request, planner(build, &planned), opener(run, &opened))
			catalogCode(t, err, tc.code)
			if bound != nil || len(planned) != 0 || build.executed != 0 || len(opened) != 0 {
				t.Fatal("admission refusal compiled, built or bound")
			}
		})
	}
}

// The production planner reaches lifecycle compilation, which refuses missing inputs before any build.
func TestBuildUsesLifecycleCompilation(t *testing.T) {
	prepared := preparedFixture(t)
	t.Chdir(t.TempDir())
	bound, err := prepared.Build(t.Context(), fixtureBuildRequest())
	var coded *e.Error
	if bound != nil || !errors.As(err, &coded) {
		t.Fatalf("missing inputs were built or uncoded: %v", err)
	}
}

// The real pre-build check compiles the Definition and refuses an output the
// Plan does not contain without creating the attempt.
func TestBuildRealPlanCheckRefusesBeforeBuilding(t *testing.T) {
	root := registrationFixture(t)
	raw := bytes.Replace(shippedContract(t), []byte("    output: nvkectl\n"), []byte("    output: missing-cli\n"), 1)
	writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/contract.yaml"), raw)
	prepared, err := Prepare(t.Context(), root, []string{"nvke/nvkectl/contract"}, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir("../../..")
	attempts := t.TempDir()
	request := BuildRequest{Definition: "docs/examples/nvke-tutorial/definition-amd64.yaml",
		Environment: "docs/examples/nvke-tutorial/environment-local-amd64.yaml", Attempt: "plan-check",
		AttemptsRoot: attempts, GeneratorVersion: "test"}
	bound, err := prepared.Build(t.Context(), request)
	catalogCode(t, err, e.CodeUsage)
	if bound != nil {
		t.Fatal("refused build was bound")
	}
	if _, statErr := os.Stat(filepath.Join(attempts, "plan-check")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused build created its attempt: %v", statErr)
	}
}
