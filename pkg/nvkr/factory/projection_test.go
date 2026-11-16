// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const projectionTestRelease = "projection-test (deadbeef)"

// faultyProjection is the reference plugin with one contract fault applied to
// its results. emits counts the Plans it received.
type faultyProjection struct {
	engine string
	fault  string
	emits  *int
}

func (plugin faultyProjection) describe(ctx context.Context) (projectionDescription, error) {
	description, err := referenceProjection{engine: plugin.engine, release: projectionTestRelease}.describe(ctx)
	switch plugin.fault {
	case "wrong-release":
		description.Release = "another adapter release"
	case "wrong-engine":
		description.Engine = "unknown"
	case "wrong-schema":
		description.PlanSchema = "unknown"
	case "wrong-abi":
		description.GeneratorABI = "unknown"
	case "wrong-protocol":
		description.Protocol = "unknown"
	}
	return description, err
}

func (plugin faultyProjection) emit(ctx context.Context, request projectionEmitRequest) (projectionEmission, error) {
	if plugin.emits != nil {
		*plugin.emits++
	}
	emission, err := referenceProjection{engine: plugin.engine}.emit(ctx, request)
	if err != nil {
		return emission, err
	}
	members := emission.Members
	switch plugin.fault {
	case "flip-byte":
		for index := range members {
			if members[index].Path == emission.WorkflowPath {
				members[index].Bytes = append([]byte(nil), members[index].Bytes...)
				members[index].Bytes[len(members[index].Bytes)/2] ^= 1
			}
		}
	case "add-member":
		members = append(members, projectionMember{Path: filepath.ToSlash(filepath.Join(filepath.Dir(emission.WorkflowPath), "zz-extra.yml")),
			Mode: projectionMemberMode, Bytes: []byte("on: push\n")})
		sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
	case "drop-member":
		for index := range members {
			if members[index].Path != emission.WorkflowPath {
				members = append(members[:index], members[index+1:]...)
				break
			}
		}
	case "escape-path":
		members[0].Path = "../escape.yml"
	case "unsorted":
		members[0], members[1] = members[1], members[0]
	case "executable-mode":
		members[0].Mode = 0o755
	case "wrong-plan-digest":
		emission.PlanDigest = "sha256:" + strings.Repeat("0", 64)
	}
	emission.Members = members
	return emission, nil
}

// projectionCalls counts in-process contract calls and Plans.
type projectionCalls struct{ calls, plans int }

type countedProjection struct {
	plugin projectionPlugin
	record *projectionCalls
}

func (counted countedProjection) describe(ctx context.Context) (projectionDescription, error) {
	counted.record.calls++
	return counted.plugin.describe(ctx)
}

func (counted countedProjection) emit(ctx context.Context, request projectionEmitRequest) (projectionEmission, error) {
	counted.record.calls++
	counted.record.plans++
	return counted.plugin.emit(ctx, request)
}

func recordProjectionCalls(t *testing.T) *projectionCalls {
	t.Helper()
	record := &projectionCalls{}
	previousBuiltin := builtinProjection
	builtinProjection = func(engine, release string) projectionPlugin {
		return countedProjection{plugin: previousBuiltin(engine, release), record: record}
	}
	t.Cleanup(func() { builtinProjection = previousBuiltin })
	return record
}

// projectionEquivalencePlans are every committed maintained Plan plus the
// flat, partitioned, development, native ARC and runtime render fixtures.
// Committed Plans load first: some fixtures change the working directory.
func projectionEquivalencePlans(t *testing.T, repo string) map[string]*Plan {
	t.Helper()
	committed, err := filepath.Glob(filepath.Join(repo, ".github", "nvkr*", "plan.json"))
	if err != nil || len(committed) != 11 {
		t.Fatalf("committed maintained Plans = %v, %v; want the eleven .github/nvkr*/plan.json", committed, err)
	}
	plans := map[string]*Plan{}
	for _, path := range committed {
		plan, err := LoadPlanContext(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		plans["committed/"+filepath.Base(filepath.Dir(path))] = plan
	}
	plans["fixture/flat"] = githubTestPlan(t)
	plans["fixture/partitioned"] = githubPartitionTestPlan(t)
	plans["fixture/development"] = githubDevelopmentTestPlan(t)
	plans["fixture/arc-amd64"] = arcTestPlan(t, "linux/amd64")
	plans["fixture/arc-arm64"] = arcTestPlan(t, "linux/arm64")
	plans["fixture/runtime"] = githubRuntimePlanFixture(t)
	return plans
}

// The in-process reference plugin emits the default in-binary bytes exactly.
func TestProjectionTransportsMatchInBinaryGenerator(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	plans := projectionEquivalencePlans(t, repo)
	names := sortedKeys(plans)
	for _, name := range names {
		plan := plans[name]
		t.Run(name, func(t *testing.T) {
			// One maintained Plan keeps real-product coverage in the default
			// suite; the whole committed matrix is an extended integration check.
			if strings.HasPrefix(name, "committed/") && name != "committed/nvkr-run-nvke-telemetry" {
				testutil.RequireExtended(t)
			}
			oracle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			// Describe then Emit: two in-process contract calls.
			for _, driver := range []struct {
				name  string
				ctx   context.Context
				calls int
			}{
				{"in-binary", t.Context(), 0},
				{"in-process", WithProjectionAdapter(t.Context(), ProjectionAdapter{Transport: ProjectionInProcess, Release: projectionTestRelease}), 2},
			} {
				record := recordProjectionCalls(t)
				got, err := Render(driver.ctx, plan, EngineGitHub)
				if err != nil {
					t.Fatalf("%s: %v", driver.name, err)
				}
				if record.calls != driver.calls {
					t.Fatalf("%s made %d in-process calls, want %d", driver.name, record.calls, driver.calls)
				}
				if !reflect.DeepEqual(got, oracle) {
					t.Fatalf("%s bundle differs from the in-binary oracle", driver.name)
				}
				if _, err := validateGeneratedInputs(t.Context(), plan, got); err != nil {
					t.Fatalf("%s: %v", driver.name, err)
				}
				if strings.HasPrefix(name, "committed/") {
					if err := CheckGeneratedBundle(t.Context(), repo, got); err != nil {
						t.Fatalf("%s differs from the committed bundle: %v", driver.name, err)
					}
				}
			}
		})
	}
}

func TestProjectionHostRefusesMisbehavingPlugins(t *testing.T) {
	for _, engine := range []string{EngineGitHub, EngineTekton} {
		t.Run(engine, func(t *testing.T) {
			plan := githubTestPlan(t)
			if engine == EngineTekton {
				plan = tektonTestPlan(t)
			}
			testProjectionHostRefusals(t, engine, plan)
		})
	}
}

func testProjectionHostRefusals(t *testing.T, engine string, plan *Plan) {
	for _, test := range []struct {
		fault string
		want  e.Code
		plans int
	}{
		{"wrong-release", e.CodeEngineCapability, 0},
		{"wrong-engine", e.CodeEngineCapability, 0},
		{"wrong-schema", e.CodeEngineCapability, 0},
		{"wrong-abi", e.CodeEngineCapability, 0},
		{"wrong-protocol", e.CodeEngineCapability, 0},
		{"flip-byte", e.CodePipelineDrift, 1},
		{"add-member", e.CodePipelineDrift, 1},
		{"drop-member", e.CodePipelineDrift, 1},
		{"escape-path", e.CodePipelineDrift, 1},
		{"unsorted", e.CodePipelineDrift, 1},
		{"executable-mode", e.CodePipelineDrift, 1},
		{"wrong-plan-digest", e.CodePipelineDrift, 1},
	} {
		t.Run(test.fault+"/in-process", func(t *testing.T) {
			emits := 0
			bundle, err := renderProjected(t.Context(), plan, engine, faultyProjection{engine: engine, fault: test.fault, emits: &emits}, projectionTestRelease)
			assertFactoryErrorCode(t, err, test.want)
			if bundle != nil {
				t.Fatal("refused projection returned a bundle")
			}
			if emits != test.plans {
				t.Fatalf("plugin received %d Plans, want %d", emits, test.plans)
			}
		})
	}
}

// Core's shape admission refuses each malformed emission before the exact
// validator runs, so every check is exercised on its own.
func TestProjectionAdmissionRefusesMalformedEmissions(t *testing.T) {
	plan := githubTestPlan(t)
	layout, planDigest := githubLayoutFor(plan), mustPlanDigest(t, plan)
	request := projectionEmitRequest{Plan: mustCanonicalPlan(t, plan), GeneratorVersion: plan.GeneratorVersion, Layout: projectionLayoutFor(layout)}
	valid, err := referenceProjection{engine: EngineGitHub}.emit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admitProjectionEmission(valid, layout, planDigest); err != nil {
		t.Fatalf("reference emission refused: %v", err)
	}
	member := func(path string) projectionMember {
		return projectionMember{Path: path, Mode: projectionMemberMode, Bytes: []byte("x")}
	}
	for _, test := range []struct {
		name   string
		mutate func(*projectionEmission)
		want   string
	}{
		{"plan digest", func(m *projectionEmission) { m.PlanDigest = "sha256:" + strings.Repeat("0", 64) }, "echoes planDigest"},
		{"workflow path", func(m *projectionEmission) { m.WorkflowPath = ".github/workflows/other.yml" }, "workflow path"},
		{"no members", func(m *projectionEmission) { m.Members = nil }, "emitted 0 members"},
		{"too many members", func(m *projectionEmission) {
			m.Members = make([]projectionMember, maxGeneratedDirEntries+1)
		}, "members"},
		{"parent", func(m *projectionEmission) { m.Members = []projectionMember{member("..")} }, "unsafe"},
		{"escape", func(m *projectionEmission) { m.Members = []projectionMember{member("../escape.yml")} }, "unsafe"},
		{"absolute", func(m *projectionEmission) { m.Members = []projectionMember{member("/etc/passwd")} }, "unsafe"},
		{"unclean", func(m *projectionEmission) { m.Members = []projectionMember{member("a//b.yml")} }, "unsafe"},
		{"too deep", func(m *projectionEmission) {
			m.Members = []projectionMember{member(strings.Repeat("d/", maxProjectionPathDepth+1) + "f.yml")}
		}, "unsafe"},
		{"duplicate", func(m *projectionEmission) { m.Members = []projectionMember{member("a.yml"), member("a.yml")} }, "sorted and unique"},
		{"unsorted", func(m *projectionEmission) { m.Members = []projectionMember{member("b.yml"), member("a.yml")} }, "sorted and unique"},
		{"mode", func(m *projectionEmission) { m.Members[0].Mode = 0o755 }, "mode"},
		{"oversized", func(m *projectionEmission) { m.Members[0].Bytes = make([]byte, maxGeneratedFileBytes+1) }, "oversized"},
	} {
		t.Run(test.name, func(t *testing.T) {
			emission := valid
			emission.Members = append([]projectionMember(nil), valid.Members...)
			test.mutate(&emission)
			files, err := admitProjectionEmission(emission, layout, planDigest)
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			if files != nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("admission refused with %v, want a %q refusal and no files", err, test.want)
			}
		})
	}
}

func mustPlanDigest(t *testing.T, plan *Plan) string {
	t.Helper()
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func mustCanonicalPlan(t *testing.T, plan *Plan) []byte {
	t.Helper()
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A selection resolves only to a known transport with its required shape, and
// a refused selection sends no Plan.
func TestProjectionSelectionAdmitsOnlyKnownTransports(t *testing.T) {
	for _, test := range []struct {
		name    string
		adapter ProjectionAdapter
	}{
		{"removed transport", ProjectionAdapter{Transport: "out-of-process", Release: projectionTestRelease}},
		{"unknown transport", ProjectionAdapter{Transport: "grpc", Release: projectionTestRelease}},
		{"no transport", ProjectionAdapter{Release: projectionTestRelease}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := recordProjectionCalls(t)
			_, err := Render(WithProjectionAdapter(t.Context(), test.adapter), githubTestPlan(t), EngineGitHub)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if record.plans != 0 {
				t.Fatal("refused selection received a Plan")
			}
		})
	}
}

func TestReferenceProjectionRefusesMalformedRequests(t *testing.T) {
	plan := githubTestPlan(t)
	valid := projectionEmitRequest{Plan: mustCanonicalPlan(t, plan), GeneratorVersion: plan.GeneratorVersion, Layout: projectionLayoutFor(githubLayoutFor(plan))}
	for _, test := range []struct {
		name   string
		change func(*projectionEmitRequest)
		code   e.Code
	}{
		{"plan bytes", func(r *projectionEmitRequest) { r.Plan = []byte("not json") }, e.CodeUsage},
		{"generator", func(r *projectionEmitRequest) { r.GeneratorVersion = "other" }, e.CodeUsage},
		{"layout", func(r *projectionEmitRequest) { r.Layout.Workflow = "other.yml" }, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.change(&request)
			emission, err := (referenceProjection{engine: EngineGitHub}).emit(t.Context(), request)
			assertFactoryErrorCode(t, err, test.code)
			if len(emission.Members) != 0 {
				t.Fatal("refused request produced output")
			}
		})
	}
}
