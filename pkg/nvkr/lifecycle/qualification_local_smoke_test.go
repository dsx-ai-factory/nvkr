// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/qualificationprogress"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// Synthetic runtime observations exercise orchestration and complete membership,
// not actual tool execution. Real build-signature tests run separately.
func checkLocalSmokeFixture(t *testing.T, workspace string, inputs *localQualificationInputs, capture *localQualificationCapture, policy LocalQualificationPolicy, document []byte) *localToolSmokeReceipt {
	t.Helper()
	_, admissionErr := runLocalToolSmoke(t.Context(), nil, NativeToolSmokeOptions{})
	assertCode(t, admissionErr, e.CodeGateUnmet)
	build := newLocalVerifiedBuild(t.Context(), inputs, capture, string(document), policy)
	members, err := localSmokeMembers(t.Context(), build)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Output != "cli" || members[0].Member != "kubectl" {
		t.Fatalf("wrong local tool scope: %+v", members)
	}
	var successful *localToolSmokeReceipt
	for _, mode := range []string{"exact", "foreign-platform", "runtime-failure", "wrong-version", "cleanup-failure", "canceled"} {
		t.Run("local smoke/"+mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var events []qualificationprogress.Event
			ctx, activity := withLocalQualificationProgress(ctx, func(event qualificationprogress.Event) { events = append(events, event) })
			activity.start(qualificationprogress.StageSmoke)
			runtime := &nativeSmokeTestRuntime{expected: append([]ToolSmokeMember(nil), members...)}
			platform := policy.Platform
			code := e.CodeEngineCapability
			switch mode {
			case "foreign-platform":
				platform = "linux/arm64"
				if policy.Platform == platform {
					platform = "linux/amd64"
				}
			case "runtime-failure":
				runtime.runHook = func(context.Context) error { return e.New(e.CodeBuildFailed, "fixture runtime failed") }
				code = e.CodeBuildFailed
			case "wrong-version":
				runtime.expected[0].ExpectedVersion = "v99.0.0"
				code = e.CodeVerifyFailed
			case "cleanup-failure":
				runtime.closeHook = func() error { return e.New(e.CodeEngineCapability, "fixture cleanup failed") }
			case "canceled":
				runtime.closeHook = func() error { cancel(); return nil }
				code = e.CodeTimeout
			}
			opened := 0
			receipt, err := runLocalToolSmokeWith(ctx, build, NativeToolSmokeOptions{}, platform, func(_ context.Context, selection pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) {
				opened++
				if selection.Image != policy.Smoke.Images[policy.Platform] || selection.Platform != policy.Platform {
					t.Fatal("runtime selection escaped independent policy")
				}
				return runtime, nil
			})
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				report, err := receipt.description(ctx)
				if err != nil || len(report.Members) != len(members) || report.Members[0].ObservedVersion != members[0].ExpectedVersion || report.BuildEvidenceDigest != pipeline.FileDigest(document) {
					t.Fatalf("wrong smoke binding: %+v %v", report, err)
				}
				if !runtime.closed || runtime.count != len(members) {
					t.Fatal("receipt preceded complete execution and cleanup")
				}
				if len(events) != 3 || events[2].Kind != qualificationprogress.KindItem || events[2].State != qualificationprogress.StateCompleted || events[2].Item != "cli/kubectl" || events[2].Completed != 1 || events[2].Total != 1 {
					t.Fatalf("smoke omitted evaluated tool progress: %+v", events)
				}
				successful = receipt
				return
			}
			assertCode(t, err, code)
			if receipt != nil {
				t.Fatal("failed smoke returned a receipt")
			}
			if mode == "foreign-platform" {
				if opened != 0 {
					t.Fatal("unsupported platform opened runtime")
				}
				if len(events) != 1 {
					t.Fatal("unsupported platform emitted tool progress")
				}
			} else if !runtime.closed {
				t.Fatal("failed smoke leaked runtime")
			}
			if mode == "runtime-failure" || mode == "wrong-version" {
				if len(events) != 2 || events[1].State != qualificationprogress.StateRunning || events[1].Completed != 0 {
					t.Fatalf("failed tool emitted completion: %+v", events)
				}
			}
		})
	}
	var empty localToolSmokeReceipt
	_, err = empty.description(t.Context())
	assertCode(t, err, e.CodeGateUnmet)
	checkLocalSmokeAuthentication(t, workspace, successful)
	checkLocalSmokeCancellation(t, build, members)
	return successful
}

func checkLocalSmokeCancellation(t *testing.T, build *localVerifiedBuild, members []ToolSmokeMember) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, cleanup, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	owner := &nativeSmokeTestRuntime{expected: members,
		runHook: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return e.Wrap(e.CodeTimeout, ctx.Err(), "fixture canceled")
		},
		closeHook: func() error { close(cleanup); <-release; return nil }}
	done := make(chan error, 1)
	go func() {
		receipt, err := runLocalToolSmokeWith(ctx, build, NativeToolSmokeOptions{}, build.policy.Platform, func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) { return owner, nil })
		if receipt != nil {
			err = e.New(e.CodeInternal, "canceled smoke returned a receipt")
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("local smoke did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- build.Close() }()
	select {
	case <-cleanup:
	case <-time.After(5 * time.Second):
		t.Fatal("closing local build did not cancel smoke")
	}
	select {
	case <-closed:
		t.Fatal("build released payloads before smoke cleanup")
	default:
	}
	unblock()
	select {
	case err := <-done:
		assertCode(t, err, e.CodeTimeout)
	case <-time.After(5 * time.Second):
		t.Fatal("smoke did not join")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("build close did not join smoke")
	}
}
