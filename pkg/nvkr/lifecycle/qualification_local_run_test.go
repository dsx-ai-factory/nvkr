// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/qualificationprogress"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestQualifyLocalRunRequiresExplicitInputs(t *testing.T) {
	for _, ctx := range []context.Context{nil, t.Context()} {
		result, err := QualifyLocalRun(ctx, LocalQualificationOptions{})
		assertCode(t, err, e.CodeUsage)
		if result == nil || result.Status != "failed" || result.FailureCode != string(e.CodeUsage) {
			t.Fatal("admission failure lost descriptive result")
		}
	}
}

// Complete entrypoint with real signatures and registry I/O. Tool observations
// are deliberately simulated; this does not exercise actual delivered tools.
func checkLocalQualificationEntrypoint(t *testing.T, workspace string, policy LocalQualificationPolicy, binary, attempt string) {
	t.Helper()
	for _, mode := range []string{"exact", "smoke-failure", "wrong-qualifier-key"} {
		t.Run("local entrypoint/"+mode, func(t *testing.T) {
			var requests atomic.Int32
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); handler.ServeHTTP(w, r) }))
			defer server.Close()
			retained := filepath.Join(".nvkr", "entrypoint-"+mode, attempt)
			if err := os.CopyFS(retained, os.DirFS(workspace)); err != nil {
				t.Fatal(err)
			}
			scratch := t.TempDir()
			policyPath := filepath.Join(scratch, "policy.json")
			writeCanonicalTestFile(t, policyPath, policy)
			options := LocalQualificationOptions{Workspace: retained, PolicyPath: policyPath, BuilderKeyPath: os.Getenv("NVKR_TEST_LOCAL_BUILDER_KEY"), SmokeKeyPath: os.Getenv("NVKR_TEST_LOCAL_SMOKE_KEY"), QualifierKeyPath: os.Getenv("NVKR_TEST_LOCAL_QUALIFIER_KEY"), CosignExecutable: binary, Repository: strings.TrimPrefix(server.URL, "http://") + "/entrypoint", Scratch: scratch, EvidenceOut: filepath.Join(scratch, "evidence.json"), Registry: delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}}
			var events []qualificationprogress.Event
			options.Progress = func(event qualificationprogress.Event) { events = append(events, event) }
			if mode == "wrong-qualifier-key" {
				options.QualifierKeyPath = options.BuilderKeyPath
			}
			plan, err := factory.LoadPlanContext(t.Context(), filepath.Join(retained, ".runner", "plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			preflightErr := preflightLocalQualification(t.Context(), plan, options, policy.Platform)
			if mode == "wrong-qualifier-key" {
				assertCode(t, preflightErr, e.CodeSignatureInvalid)
			} else if preflightErr != nil {
				t.Fatal(preflightErr)
			}
			if requests.Load() != 0 {
				t.Fatal("preflight performed registry I/O")
			}
			if _, err := os.Stat(options.EvidenceOut); !os.IsNotExist(err) {
				t.Fatal("preflight wrote qualification evidence")
			}
			calls := 0
			smoke := func(ctx context.Context, build *localVerifiedBuild, opts NativeToolSmokeOptions) (*localToolSmokeReceipt, error) {
				calls++
				members, err := localSmokeMembers(ctx, build)
				if err != nil {
					return nil, err
				}
				runtime := &nativeSmokeTestRuntime{expected: members}
				if mode == "smoke-failure" {
					runtime.runHook = func(context.Context) error { return e.New(e.CodeBuildFailed, "injected smoke refusal") }
				}
				return runLocalToolSmokeWith(ctx, build, opts, policy.Platform, func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) { return runtime, nil })
			}
			result, err := qualifyLocalRunWith(t.Context(), options, policy.Platform, smoke)
			var started, completed []string
			for _, event := range events {
				if err := event.Validate(); err != nil {
					t.Fatal(err)
				}
				if event.Kind == qualificationprogress.KindStage {
					switch event.State {
					case qualificationprogress.StateRunning:
						started = append(started, event.Stage)
					case qualificationprogress.StateCompleted:
						completed = append(completed, event.Stage)
					}
				}
			}
			if mode != "exact" {
				code := e.CodeBuildFailed
				if mode == "wrong-qualifier-key" {
					code = e.CodeSignatureInvalid
				}
				assertCode(t, err, code)
				if result.Status != "failed" || requests.Load() != 0 || result.Publication.Evidence != "" {
					t.Fatal("failed local gate published or passed")
				}
				if _, err := os.Stat(options.EvidenceOut); !os.IsNotExist(err) {
					t.Fatal("failed gate wrote qualification evidence")
				}
				if mode == "wrong-qualifier-key" && calls != 0 {
					t.Fatal("invalid signing key reached smoke")
				}
				failedStage := qualificationprogress.StageSmoke
				if mode == "wrong-qualifier-key" {
					failedStage = qualificationprogress.StageInputs
				}
				if len(events) == 0 || events[len(events)-1].Stage != failedStage || events[len(events)-1].State != qualificationprogress.StateFailed || slices.Contains(completed, failedStage) {
					t.Fatalf("failure lost active stage or reported completion: %+v", events)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "passed" || result.Attempt != attempt || result.Publication.Evidence == "" || result.Lane != "unqualified-local-execution" || calls != 1 {
				t.Fatal("complete local entrypoint lost result", result)
			}
			wantStages := []string{"inputs", "builder", "smoke", "qualifier", "publish", "readback"}
			if !slices.Equal(started, wantStages) || !slices.Equal(completed, wantStages) {
				t.Fatalf("incorrect stage sequence: started=%v completed=%v", started, completed)
			}
			if _, err := readLocalEvidenceBundle(t.Context(), options.EvidenceOut); err != nil {
				t.Fatal(err)
			}
		})
	}
}
