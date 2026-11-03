// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeIdentityReadinessFixture(t *testing.T) (*nativeSiteAttempt, Runner, string, *recordedProgress) {
	t.Helper()
	root := privateTestDir(t)
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	attempt := &nativeSiteAttempt{bridge: linuxNativeBridge(), assets: NativeAssets{HostTools: tools, Images: map[string]NativeImageAsset{}}}
	installNativeIdentityFixture(t, attempt, tools, root)
	writeTenantFixture(t, filepath.Join(root, "site-prepared.json"), []byte(`{"tpmEkThumbprint":"`+strings.Repeat("a", 64)+`"}`), 0600)
	raw, err := os.ReadFile(filepath.Join(root, "identity-pods.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pods nativeIdentityPodList
	if err := json.Unmarshal(raw, &pods); err != nil {
		t.Fatal(err)
	}
	pods.Items[0].Status.ContainerStatuses[0].Ready = false
	if err := privateJSON(filepath.Join(root, "identity-pods-unready.json"), pods); err != nil {
		t.Fatal(err)
	}
	progress := &recordedProgress{}
	runner := Runner{Work: root, Env: []string{"PATH=" + tools + ":/usr/bin:/bin", "FIXTURE=" + root, "EVENTS=" + filepath.Join(root, "events"), "IDENTITY_POLICY_UNREADY=once"}, Progress: progress}
	return attempt, runner, root, progress
}

func TestNativeIdentityPolicyConvergesReadinessAfterOneCreate(t *testing.T) {
	for _, scenario := range []string{"controller", "server", "both", "observation retry"} {
		t.Run(scenario, func(t *testing.T) {
			convergeNativeIdentityPolicy(t, scenario)
		})
	}
}

func convergeNativeIdentityPolicy(t *testing.T, scenario string) {
	t.Helper()
	attempt, runner, root, progress := nativeIdentityReadinessFixture(t)
	if scenario == "observation retry" {
		runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_POLICY_UNREADY": "", "IDENTITY_POLICY_OBSERVE_FAILURE": "once"})
	} else if scenario != "controller" {
		rewriteIdentityObservation(t, root, "identity-pods-unready.json", "identity-pods-unready.json", func(document map[string]any) {
			pods := document["Items"].([]any)
			pods[0].(map[string]any)["Status"].(map[string]any)["ContainerStatuses"].([]any)[0].(map[string]any)["Ready"] = scenario != "both"
			pods[1].(map[string]any)["Status"].(map[string]any)["ContainerStatuses"].([]any)[0].(map[string]any)["Ready"] = false
		})
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	receipt, err := attempt.completeNativeSiteIdentity(ctx, runner, "172.30.0.2", root)
	if err != nil || receipt == nil {
		t.Fatal("same pinned running container did not converge after temporary readiness loss", err)
	}
	assertNativeIdentityFixtureCompletion(t, root)
	events, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil || strings.Count(string(events), "identity-import\n") != 1 || strings.Count(string(events), "identity-handoff\n") != 1 {
		t.Fatal("policy convergence retried create or mutation", err)
	}
	for _, event := range progress.events {
		if event.Kind == PhaseFailed {
			t.Fatal("retryable readiness wait emitted a failed phase", event.Name)
		}
	}

}

func rewriteIdentityObservation(t *testing.T, root, source, target string, edit func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, source))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	edit(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeTenantFixture(t, filepath.Join(root, target), encoded, 0600)
}

func assertIdentityPolicyNotDispatched(t *testing.T, root string) {
	t.Helper()
	events, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil || strings.Count(string(events), "identity-import\n") != 1 || strings.Contains(string(events), "identity-handoff") {
		t.Fatal("failed policy changed create-once or dispatched mutation", err)
	}
	receipts, err := filepath.Glob(filepath.Join(root, "site-identity-*", "completed.json"))
	if err != nil || len(receipts) != 0 {
		t.Fatal("failed policy issued completion", err)
	}
}

func TestNativeIdentityUnreadyRecheckRejectsChangedContext(t *testing.T) {
	for _, scenario := range []string{"controller UID", "server UID", "controller container", "server container", "controller image", "not running", "config UID", "config bytes", "boot ID", "CSI", "staging", "server mount", "service binding", "API error"} {
		t.Run(scenario, func(t *testing.T) {
			attempt, runner, root, _ := nativeIdentityReadinessFixture(t)
			runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_POLICY_UNREADY": "always"})
			switch scenario {
			case "controller UID", "server UID", "controller container", "server container", "controller image", "not running", "CSI", "staging", "server mount":
				rewriteIdentityObservation(t, root, "identity-pods-unready.json", "identity-pods-unready.json", func(document map[string]any) {
					index := 0
					if strings.HasPrefix(scenario, "server") {
						index = 1
					}
					pod := document["Items"].([]any)[index].(map[string]any)
					switch scenario {
					case "controller UID", "server UID":
						pod["Metadata"].(map[string]any)["UID"] = "replacement-private-uid"
					case "controller container", "server container":
						pod["Status"].(map[string]any)["ContainerStatuses"].([]any)[0].(map[string]any)["ContainerID"] = "containerd://replacement-private-id"
					case "controller image":
						pod["Status"].(map[string]any)["ContainerStatuses"].([]any)[0].(map[string]any)["ImageID"] = "other"
					case "not running":
						pod["Status"].(map[string]any)["ContainerStatuses"].([]any)[0].(map[string]any)["State"] = map[string]any{}
					case "CSI", "staging", "server mount":
						container := pod["Spec"].(map[string]any)["Containers"].([]any)[0].(map[string]any)
						mounts := container["VolumeMounts"].([]any)
						mount := 0
						if scenario == "staging" {
							mount = 2
						}
						mounts[mount].(map[string]any)["MountPath"] = "/unsupported-private-mount"
					}
				})
			case "config UID", "config bytes":
				rewriteIdentityObservation(t, root, "identity-config.json", "identity-config-policy.json", func(document map[string]any) {
					if scenario == "config UID" {
						document["metadata"].(map[string]any)["uid"] = "replacement-private-uid"
					} else {
						data := document["data"].(map[string]any)
						data["config.yaml"] = data["config.yaml"].(string) + "# changed bytes\n"
					}
				})
			case "boot ID":
				rewriteIdentityObservation(t, root, "identity-nodes.json", "identity-nodes-policy.json", func(document map[string]any) {
					node := document["items"].([]any)[0].(map[string]any)
					node["status"].(map[string]any)["nodeInfo"].(map[string]any)["bootID"] = "replacement-private-boot"
				})
			case "service binding":
				rewriteIdentityObservation(t, root, "identity-services.json", "identity-services-policy.json", func(document map[string]any) {
					service := document["items"].([]any)[0].(map[string]any)
					service["spec"].(map[string]any)["selector"] = map[string]any{"app": "other"}
				})
			case "API error":
				runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_POLICY_UNREADY": "api-error"})
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			receipt, err := attempt.completeNativeSiteIdentity(ctx, runner, "172.30.0.2", root)
			if err == nil || receipt != nil || errors.Is(err, errNativeIdentityUnready) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("changed identity was treated as retryable readiness", err)
			}
			if scenario == "API error" && CommandExitCode(err) != 17 {
				t.Fatal("runtime API failure lost original exit code", err)
			}
			polls, readErr := os.ReadFile(filepath.Join(root, "identity-policy-polls"))
			if readErr != nil || string(polls) != "1\n" {
				t.Fatal("changed context or API error was retried", readErr)
			}
			assertIdentityPolicyNotDispatched(t, root)
		})
	}
}

func TestNativeIdentityPolicyCreateFailurePreventsMutation(t *testing.T) {
	for _, scenario := range []string{"create", "created-response"} {
		t.Run(scenario, func(t *testing.T) {
			attempt, runner, root, _ := nativeIdentityReadinessFixture(t)
			if scenario == "create" {
				runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_IMPORT_FAILURE": "1"})
			} else {
				writeTenantFixture(t, filepath.Join(root, "identity-created.json"), []byte(`{"privateFixture":"untrusted-response"}`), 0600)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			receipt, err := attempt.completeNativeSiteIdentity(ctx, runner, "172.30.0.2", root)
			if err == nil || receipt != nil {
				t.Fatal("failed create or response admitted policy")
			}
			assertIdentityPolicyNotDispatched(t, root)
		})
	}
}

func TestNativeIdentityPolicyReadinessBoundAndCancellation(t *testing.T) {
	for _, scenario := range []string{"deadline", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			attempt, runner, root, _ := nativeIdentityReadinessFixture(t)
			runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_POLICY_UNREADY": "always"})
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			want := context.DeadlineExceeded
			if scenario == "cancel" {
				// Observe a completed unready iteration before canceling a later read/pause.
				done := make(chan struct{})
				go func() {
					defer close(done)
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							polls, _ := os.ReadFile(filepath.Join(root, "identity-policy-polls"))
							if strings.TrimSpace(string(polls)) == "2" {
								cancel()
								return
							}
						}
					}
				}()
				defer func() { cancel(); <-done }()
				want = context.Canceled
			}
			var err error
			if scenario == "cancel" {
				var receipt *nativeSiteIdentityReceipt
				receipt, err = attempt.completeNativeSiteIdentity(ctx, runner, "172.30.0.2", root)
				if receipt != nil {
					t.Fatal("canceled identity completion issued a receipt")
				}
				assertIdentityPolicyNotDispatched(t, root)
			} else {
				sourceRaw, readErr := os.ReadFile(filepath.Join(root, "clusterstaticentries.yaml"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				tpm := strings.Repeat("a", 64)
				source, spec, sourceErr := nativeSourceStaticEntry(sourceRaw, tpm)
				if sourceErr != nil {
					t.Fatal(sourceErr)
				}
				k := nativeIdentityCommands{runner: runner, kubectl: filepath.Join(attempt.assets.HostTools, "kubectl"), kubeconfig: "unused"}
				runtime, runtimeErr := k.runtime(ctx, attempt.assets, spec, "172.30.0.2")
				if runtimeErr != nil {
					t.Fatal(runtimeErr)
				}
				// Keep fixture setup outside the policy wait's deadline budget.
				writeTenantFixture(t, filepath.Join(root, "identity-imported"), nil, 0600)
				waitCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				defer stop()
				err = k.waitPolicy(waitCtx, runtime, attempt.assets, spec, "172.30.0.2", source, tpm, "created-fixture")
			}
			polls, pollErr := os.ReadFile(filepath.Join(root, "identity-policy-polls"))
			if !errors.Is(err, want) || !errors.Is(err, errNativeIdentityUnready) || pollErr != nil || len(polls) == 0 {
				t.Fatal("unready wait lost its bound, cause or observed poll", err, pollErr)
			}
		})
	}
}

func TestNativeIdentityReadyAdmissionAndPostPolicyLoss(t *testing.T) {
	for _, scenario := range []string{"initial unready", "after policy"} {
		t.Run(scenario, func(t *testing.T) {
			attempt, runner, root, _ := nativeIdentityReadinessFixture(t)
			if scenario == "initial unready" {
				raw, err := os.ReadFile(filepath.Join(root, "identity-pods-unready.json"))
				if err != nil {
					t.Fatal(err)
				}
				writeTenantFixture(t, filepath.Join(root, "identity-pods.json"), raw, 0600)
			} else {
				runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_POLICY_UNREADY": "after-policy"})
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			receipt, err := attempt.completeNativeSiteIdentity(ctx, runner, "172.30.0.2", root)
			if receipt != nil || !errors.Is(err, errNativeIdentityUnready) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("unready runtime crossed initial or mutation admission", err)
			}
			if scenario == "initial unready" {
				if _, err := os.Stat(filepath.Join(root, "identity-imported")); !os.IsNotExist(err) {
					t.Fatal("initial unready runtime reached policy creation")
				}
			} else {
				if _, err := os.Stat(filepath.Join(root, "identity-verified")); err != nil {
					t.Fatal("test did not reach final pre-mutation recheck", err)
				}
				assertIdentityPolicyNotDispatched(t, root)
			}
		})
	}
}

func TestNativeIdentityReadinessWaitPreservesOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		wait bool
	}{
		{"no failure", nil, false},
		{"issued pending condition", errNativeIdentityUnready, true},
		{"wrapped condition", fmt.Errorf("other boundary: %w", errNativeIdentityUnready), false},
		{"unjoined condition", errors.Join(errNativeIdentityUnready, ErrUnjoined), false},
		{"API failure", errors.New("synthetic private API failure"), false},
		{"canceled", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if nativeIdentityReadinessPending(tc.err) != tc.wait {
				t.Fatal("non-readiness failure became a policy wait")
			}
		})
	}
}
