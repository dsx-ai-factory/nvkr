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
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The out-of-process projection pilot: generation drivers (in-binary Render
// and the subprocess adapter behind the same seam) share the RenderGitHub
// oracle, and the host refuses every misbehaving adapter with a coded error.

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
	return ProjectionAdapter{Path: self, Args: []string{projectionTestAdapterArg, fault}, Release: projectionTestRelease}
}

// serveProjectionTestAdapter runs the reference adapter and applies one fault.
func serveProjectionTestAdapter(fault string) int {
	switch fault {
	case "hang":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Hour)
		return 1
	case "refuse":
		fmt.Fprintln(os.Stderr, "adapter refuses this Plan")
		return 3
	case "oversized-frame":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{0xff}, 4+maxProjectionDescriptionBytes+1))
		return 0
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var out bytes.Buffer
	if err := ServeProjectionAdapter(context.Background(), projectionTestRelease, bytes.NewReader(raw), &out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	frame, err := applyProjectionFault(fault, raw, out.Bytes())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, _ = os.Stdout.Write(frame)
	return 0
}

func applyProjectionFault(fault string, request, frame []byte) ([]byte, error) {
	var decoded projectionRequest
	if err := decodeProjectionFrame(request, &decoded, e.CodeUsage); err != nil {
		return nil, err
	}
	if decoded.Emit == nil {
		if fault != "wrong-release" {
			return frame, nil
		}
		var description projectionDescription
		if err := decodeProjectionFrame(frame, &description, e.CodeUsage); err != nil {
			return nil, err
		}
		description.Release = "another adapter release"
		return encodeProjectionFrame(description)
	}
	var emission projectionEmission
	if err := decodeProjectionFrame(frame, &emission, e.CodeUsage); err != nil {
		return nil, err
	}
	members := emission.Members
	switch fault {
	case "flip-byte":
		for index := range members {
			if members[index].Path == emission.WorkflowPath {
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
	frame, err := encodeProjectionFrame(emission)
	if err != nil {
		return nil, err
	}
	reframe := func(raw []byte) ([]byte, error) {
		return append([]byte{byte(len(raw) >> 24), byte(len(raw) >> 16), byte(len(raw) >> 8), byte(len(raw))}, raw...), nil
	}
	switch fault {
	case "trailing-bytes":
		return append(frame, '\n'), nil
	case "truncated":
		return frame[:len(frame)-1], nil
	case "unknown-field":
		return reframe(append([]byte(`{"extra":true,`), frame[5:]...))
	case "non-canonical":
		return reframe(append([]byte(`{ `), frame[5:]...))
	}
	return frame, nil
}

// projectionLaunches counts adapter launches and how many carried a Plan.
type projectionLaunches struct{ launches, plans int }

func recordProjectionLaunches(t *testing.T) *projectionLaunches {
	t.Helper()
	previous, record := launchProjectionAdapter, &projectionLaunches{}
	launchProjectionAdapter = func(ctx context.Context, adapter ProjectionAdapter, request []byte, limit int) ([]byte, error) {
		record.launches++
		var decoded projectionRequest
		if decodeProjectionFrame(request, &decoded, e.CodeUsage) == nil && decoded.Emit != nil {
			record.plans++
		}
		return previous(ctx, adapter, request, limit)
	}
	t.Cleanup(func() { launchProjectionAdapter = previous })
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

func TestProjectionAdapterMatchesInBinaryGenerator(t *testing.T) {
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
			for _, driver := range []struct {
				name     string
				ctx      context.Context
				launches int
			}{
				{"in-binary", t.Context(), 0},
				{"subprocess", WithProjectionAdapter(t.Context(), projectionTestAdapter(t, "reference")), 2},
			} {
				record := recordProjectionLaunches(t)
				got, err := Render(driver.ctx, plan, EngineGitHub)
				if err != nil {
					t.Fatalf("%s: %v", driver.name, err)
				}
				if record.launches != driver.launches {
					t.Fatalf("%s launched the adapter %d times, want %d", driver.name, record.launches, driver.launches)
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

func TestProjectionHostRefusesMisbehavingAdapters(t *testing.T) {
	plan := githubTestPlan(t)
	for _, test := range []struct {
		fault string
		want  e.Code
		plans int
	}{
		{"wrong-release", e.CodeEngineCapability, 0},
		{"refuse", e.CodeEngineCapability, 0},
		{"oversized-frame", e.CodePipelineDrift, 0},
		{"hang", e.CodeTimeout, 0},
		{"flip-byte", e.CodePipelineDrift, 1},
		{"add-member", e.CodePipelineDrift, 1},
		{"drop-member", e.CodePipelineDrift, 1},
		{"escape-path", e.CodePipelineDrift, 1},
		{"unsorted", e.CodePipelineDrift, 1},
		{"executable-mode", e.CodePipelineDrift, 1},
		{"wrong-plan-digest", e.CodePipelineDrift, 1},
		{"trailing-bytes", e.CodePipelineDrift, 1},
		{"truncated", e.CodePipelineDrift, 1},
		{"unknown-field", e.CodePipelineDrift, 1},
		{"non-canonical", e.CodePipelineDrift, 1},
	} {
		t.Run(test.fault, func(t *testing.T) {
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
	}
}

func TestProjectionHostStopsAdapterOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	timer := time.AfterFunc(200*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	_, err := Render(WithProjectionAdapter(ctx, projectionTestAdapter(t, "hang")), githubTestPlan(t), EngineGitHub)
	assertFactoryErrorCode(t, err, e.CodeTimeout)
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("canceled adapter was not reaped promptly: %s", elapsed)
	}
}

func TestProjectionHostAdmitsOnlyAbsoluteAdapters(t *testing.T) {
	record := recordProjectionLaunches(t)
	_, err := Render(WithProjectionAdapter(t.Context(), ProjectionAdapter{Path: "nvkr", Release: projectionTestRelease}), githubTestPlan(t), EngineGitHub)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if record.plans != 0 {
		t.Fatal("relative adapter received a Plan")
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
