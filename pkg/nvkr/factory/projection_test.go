// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The projection pilot: the default in-binary Render and the reference plugin
// reached in-process and out-of-process behind the same seam share the
// RenderGitHub oracle, and core refuses every misbehaving plugin with a coded
// error on either transport.

const (
	projectionTestAdapterArg = "nvkr-projection-test-adapter"
	projectionTestRelease    = "projection-test (deadbeef)"
)

// TestMain lets this test binary serve as the reference adapter process. The
// host launches it with an empty environment, so argv selects the mode.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == projectionTestAdapterArg {
		os.Exit(serveProjectionTestAdapter(os.Args[2]))
	}
	os.Exit(m.Run())
}

func projectionTestAdapter(t *testing.T, fault string) ProjectionAdapter {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ProjectionAdapter{Transport: ProjectionOutOfProcess, Path: self,
		Args: []string{projectionTestAdapterArg, fault}, Release: projectionTestRelease}
}

// faultyProjection is the reference plugin with one contract fault applied to
// its results. Both transports serve it, so core's admission is shown to be
// transport-neutral. emits counts the Plans it received in-process.
type faultyProjection struct {
	fault string
	emits *int
}

func (plugin faultyProjection) describe(ctx context.Context) (projectionDescription, error) {
	description, err := referenceProjection{release: projectionTestRelease}.describe(ctx)
	if plugin.fault == "wrong-release" {
		description.Release = "another adapter release"
	}
	return description, err
}

func (plugin faultyProjection) emit(ctx context.Context, request projectionEmitRequest) (projectionEmission, error) {
	if plugin.emits != nil {
		*plugin.emits++
	}
	emission, err := referenceProjection{}.emit(ctx, request)
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

// serveProjectionTestAdapter serves faultyProjection out of process and then
// applies one process or framing fault.
func serveProjectionTestAdapter(fault string) int {
	// The host owes every launch an empty environment and a private empty
	// working directory; anything else fails the launch.
	wd, err := os.Getwd()
	entries, readErr := os.ReadDir(".")
	if len(os.Environ()) != 0 || err != nil || readErr != nil || len(entries) != 0 || !strings.HasPrefix(filepath.Base(wd), "nvkr-projection-") {
		fmt.Fprintf(os.Stderr, "adapter launched with environment %q in %q\n", os.Environ(), wd)
		return 5
	}
	switch fault {
	case "hang":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Hour)
		return 1
	case "refuse":
		fmt.Fprintln(os.Stderr, "adapter refuses this Plan")
		return 3
	case "oversized-frame":
		// A well-formed Describe frame past the bound: only the stdout bound
		// refuses it as drift; decoded, it would be a release mismatch.
		frame, err := encodeProjectionFrame(projectionDescription{Protocol: projectionProtocol,
			Release: strings.Repeat("x", maxProjectionDescriptionBytes)})
		if err != nil {
			return 1
		}
		_, _ = os.Stdout.Write(frame)
		return 0
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var out bytes.Buffer
	if err := serveProjection(context.Background(), faultyProjection{fault: fault}, bytes.NewReader(raw), &out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	frame := out.Bytes()
	// Framing faults corrupt the Emit response, after the Plan was sent.
	var request projectionRequest
	if decodeProjectionFrame(raw, &request, e.CodeUsage) == nil && request.Emit != nil {
		frame = applyProjectionFrameFault(fault, frame)
	}
	_, _ = os.Stdout.Write(frame)
	return 0
}

func applyProjectionFrameFault(fault string, frame []byte) []byte {
	reframe := func(raw []byte) []byte {
		return append([]byte{byte(len(raw) >> 24), byte(len(raw) >> 16), byte(len(raw) >> 8), byte(len(raw))}, raw...)
	}
	switch fault {
	case "trailing-bytes":
		return append(frame, '\n')
	case "truncated":
		return frame[:len(frame)-1]
	case "unknown-field":
		return reframe(append([]byte(`{"extra":true,`), frame[5:]...))
	case "non-canonical":
		return reframe(append([]byte(`{ `), frame[5:]...))
	}
	return frame
}

// projectionLaunches counts out-of-process launches, in-process contract
// calls, and how many of either carried a Plan.
type projectionLaunches struct{ launches, calls, plans int }

type countedProjection struct {
	plugin projectionPlugin
	record *projectionLaunches
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

func recordProjectionLaunches(t *testing.T) *projectionLaunches {
	t.Helper()
	previous, record := launchProjectionAdapter, &projectionLaunches{}
	previousBuiltin := builtinProjection
	builtinProjection = func(release string) projectionPlugin {
		return countedProjection{plugin: previousBuiltin(release), record: record}
	}
	launchProjectionAdapter = func(ctx context.Context, adapter ProjectionAdapter, request []byte, limit int) ([]byte, error) {
		record.launches++
		var decoded projectionRequest
		if decodeProjectionFrame(request, &decoded, e.CodeUsage) == nil && decoded.Emit != nil {
			record.plans++
		}
		return previous(ctx, adapter, request, limit)
	}
	t.Cleanup(func() { launchProjectionAdapter, builtinProjection = previous, previousBuiltin })
	return record
}

// projectionEquivalencePlans are every committed maintained Plan plus the
// flat, partitioned, development, native ARC and runtime render fixtures.
// Committed Plans load first: some fixtures change the working directory.
func projectionEquivalencePlans(t *testing.T, repo string) map[string]*Plan {
	t.Helper()
	committed, err := filepath.Glob(filepath.Join(repo, ".github", "nvkr*", "plan.json"))
	if err != nil || len(committed) != 8 {
		t.Fatalf("committed maintained Plans = %v, %v; want the eight .github/nvkr*/plan.json", committed, err)
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

// The same reference plugin runs in-process and out-of-process through one
// contract, and both transports emit the default in-binary bytes exactly.
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
			// Describe then Emit: two in-process contract calls or two launches.
			for _, driver := range []struct {
				name            string
				ctx             context.Context
				launches, calls int
			}{
				{"in-binary", t.Context(), 0, 0},
				{"in-process", WithProjectionAdapter(t.Context(), ProjectionAdapter{Transport: ProjectionInProcess, Release: projectionTestRelease}), 0, 2},
				{"out-of-process", WithProjectionAdapter(t.Context(), projectionTestAdapter(t, "reference")), 2, 0},
			} {
				record := recordProjectionLaunches(t)
				got, err := Render(driver.ctx, plan, EngineGitHub)
				if err != nil {
					t.Fatalf("%s: %v", driver.name, err)
				}
				if record.launches != driver.launches || record.calls != driver.calls {
					t.Fatalf("%s made %d launches and %d in-process calls, want %d and %d",
						driver.name, record.launches, record.calls, driver.launches, driver.calls)
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

func TestProjectionAdapterOutputIgnoresHostEnvironment(t *testing.T) {
	plan := githubTestPlan(t)
	adapter := projectionTestAdapter(t, "reference")
	first, err := Render(WithProjectionAdapter(t.Context(), adapter), plan, EngineGitHub)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("NVKR_WORK_DEADLINE", "2000-01-01T00:00:00Z")
	t.Chdir(t.TempDir())
	second, err := Render(WithProjectionAdapter(t.Context(), adapter), plan, EngineGitHub)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("projection adapter output changed with the host environment or working directory")
	}
}

// Contract faults run on both transports; process and framing faults exist
// only out of process. Every refusal is coded and returns no bundle.
func TestProjectionHostRefusesMisbehavingPlugins(t *testing.T) {
	plan := githubTestPlan(t)
	for _, test := range []struct {
		fault    string
		want     e.Code
		plans    int
		contract bool
	}{
		{"wrong-release", e.CodeEngineCapability, 0, true},
		{"refuse", e.CodeEngineCapability, 0, false},
		{"oversized-frame", e.CodePipelineDrift, 0, false},
		{"hang", e.CodeTimeout, 0, false},
		{"flip-byte", e.CodePipelineDrift, 1, true},
		{"add-member", e.CodePipelineDrift, 1, true},
		{"drop-member", e.CodePipelineDrift, 1, true},
		{"escape-path", e.CodePipelineDrift, 1, true},
		{"unsorted", e.CodePipelineDrift, 1, true},
		{"executable-mode", e.CodePipelineDrift, 1, true},
		{"wrong-plan-digest", e.CodePipelineDrift, 1, true},
		{"trailing-bytes", e.CodePipelineDrift, 1, false},
		{"truncated", e.CodePipelineDrift, 1, false},
		{"unknown-field", e.CodePipelineDrift, 1, false},
		{"non-canonical", e.CodePipelineDrift, 1, false},
	} {
		t.Run(test.fault+"/out-of-process", func(t *testing.T) {
			record := recordProjectionLaunches(t)
			adapter := projectionTestAdapter(t, test.fault)
			if test.fault == "hang" {
				adapter.Timeout = 300 * time.Millisecond
			}
			started := time.Now()
			bundle, err := Render(WithProjectionAdapter(t.Context(), adapter), plan, EngineGitHub)
			assertFactoryErrorCode(t, err, test.want)
			if bundle != nil {
				t.Fatal("refused projection returned a bundle")
			}
			if record.plans != test.plans {
				t.Fatalf("adapter received %d Plans, want %d", record.plans, test.plans)
			}
			if elapsed := time.Since(started); elapsed > 30*time.Second {
				t.Fatalf("refusal took %s", elapsed)
			}
		})
		if !test.contract {
			continue
		}
		t.Run(test.fault+"/in-process", func(t *testing.T) {
			record, emits := recordProjectionLaunches(t), 0
			bundle, err := renderProjected(t.Context(), plan, faultyProjection{fault: test.fault, emits: &emits}, projectionTestRelease)
			assertFactoryErrorCode(t, err, test.want)
			if bundle != nil {
				t.Fatal("refused projection returned a bundle")
			}
			if emits != test.plans || record.launches != 0 {
				t.Fatalf("plugin received %d Plans with %d launches, want %d and none", emits, record.launches, test.plans)
			}
		})
	}
}

func TestProjectionHostSeparatesRefusalFromLaunchFailure(t *testing.T) {
	plan := githubTestPlan(t)
	missing := projectionTestAdapter(t, "refuse")
	missing.Path = filepath.Join(t.TempDir(), "absent")
	for _, test := range []struct {
		name, want, not string
		adapter         ProjectionAdapter
	}{
		{"refusal", "projection adapter refused", "host failed", projectionTestAdapter(t, "refuse")},
		{"missing adapter", "host failed to run projection adapter", "refused", missing},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Render(WithProjectionAdapter(t.Context(), test.adapter), plan, EngineGitHub)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), test.not) {
				t.Fatalf("error %q, want %q without %q", err, test.want, test.not)
			}
		})
	}
}

func TestProjectionHostStopsAdapterOnCancellation(t *testing.T) {
	plan, record := githubTestPlan(t), recordProjectionLaunches(t)
	ctx, cancel := context.WithCancel(t.Context())
	timer := time.AfterFunc(200*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	_, err := Render(WithProjectionAdapter(ctx, projectionTestAdapter(t, "hang")), plan, EngineGitHub)
	assertFactoryErrorCode(t, err, e.CodeTimeout)
	if record.launches != 1 {
		t.Fatalf("cancellation reached %d adapter launches, want 1", record.launches)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("canceled adapter was not reaped promptly: %s", elapsed)
	}
}

// Core's shape admission refuses each malformed emission before the exact
// validator runs, so every check is exercised on its own.
func TestProjectionAdmissionRefusesMalformedEmissions(t *testing.T) {
	plan := githubTestPlan(t)
	layout, planDigest := githubLayoutFor(plan), mustPlanDigest(t, plan)
	request := projectionEmitRequest{Plan: mustCanonicalPlan(t, plan), GeneratorVersion: plan.GeneratorVersion, Layout: projectionLayoutFor(layout)}
	valid, err := referenceProjection{}.emit(t.Context(), request)
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
		{"relative adapter", ProjectionAdapter{Transport: ProjectionOutOfProcess, Path: "nvkr", Release: projectionTestRelease}},
		{"in-process executable", ProjectionAdapter{Transport: ProjectionInProcess, Path: "/bin/true", Release: projectionTestRelease}},
		{"unknown transport", ProjectionAdapter{Transport: "grpc", Release: projectionTestRelease}},
		{"no transport", ProjectionAdapter{Path: "/bin/true", Release: projectionTestRelease}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := recordProjectionLaunches(t)
			_, err := Render(WithProjectionAdapter(t.Context(), test.adapter), githubTestPlan(t), EngineGitHub)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if record.plans != 0 {
				t.Fatal("refused selection received a Plan")
			}
		})
	}
}

func TestReferenceAdapterRefusesMalformedRequests(t *testing.T) {
	plan := githubTestPlan(t)
	planBytes, err := encodeProjectionFrame(plan)
	if err != nil {
		t.Fatal(err)
	}
	layout := projectionLayoutFor(githubLayoutFor(plan))
	for _, test := range []struct {
		name    string
		request any
		want    e.Code
	}{
		{"protocol", projectionRequest{Protocol: "projection/v1"}, e.CodeEngineCapability},
		{"plan bytes", projectionRequest{Protocol: projectionProtocol, Emit: &projectionEmitRequest{
			Plan: []byte("not json"), GeneratorVersion: plan.GeneratorVersion, Layout: layout}}, e.CodeUsage},
		{"generator", projectionRequest{Protocol: projectionProtocol, Emit: &projectionEmitRequest{
			Plan: planBytes[4:], GeneratorVersion: "other", Layout: layout}}, e.CodeUsage},
		{"layout", projectionRequest{Protocol: projectionProtocol, Emit: &projectionEmitRequest{
			Plan: planBytes[4:], GeneratorVersion: plan.GeneratorVersion, Layout: projectionLayout{Workflow: "w", Manifest: "m", Plan: "p"}}}, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			frame, err := encodeProjectionFrame(test.request)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = ServeProjectionAdapter(t.Context(), projectionTestRelease, bytes.NewReader(frame), &out)
			assertFactoryErrorCode(t, err, test.want)
			if out.Len() != 0 {
				t.Fatal("refused request produced output")
			}
		})
	}
}
