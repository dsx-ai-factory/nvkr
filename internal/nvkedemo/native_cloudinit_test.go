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

const cleanNativeCloudInit = `{"status":"done","extended_status":"done","errors":[],"recoverable_errors":{}}`

func TestNativeCloudInitUsesDocumentedJSONAndRejectsUnknownCompletion(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"clean", cleanNativeCloudInit},
		{"disabled", `{"status":"disabled","extended_status":"disabled","errors":[],"recoverable_errors":{}}`},
		{"running", `{"status":"running","extended_status":"running","errors":[],"recoverable_errors":{}}`},
		{"degraded", `{"status":"done","extended_status":"degraded done","errors":[],"recoverable_errors":{"warning":["failure"]}}`},
		{"errors", `{"status":"done","extended_status":"done","errors":["failure"],"recoverable_errors":{}}`},
		{"missing fields", `{"status":"done"}`},
		{"wrong types", `{"status":"done","extended_status":"done","errors":"none","recoverable_errors":{}}`},
		{"duplicate status", `{"status":"error","status":"done","extended_status":"done","errors":[],"recoverable_errors":{}}`},
		{"trailing document", cleanNativeCloudInit + `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := nativeCloudInitStatus([]byte(tc.raw)); (err == nil) != (tc.name == "clean") {
				t.Fatalf("status result=%v", err)
			}
		})
	}
}

func TestNativeCloudInitBindsActualExecutionAndJoinsUIDCleanup(t *testing.T) {
	for _, count := range []int{2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) { nativeCloudInitExecutionCases(t, count) })
	}
}

func nativeCloudInitExecutionCases(t *testing.T, count int) {
	image := "example.invalid/selected-workload@sha256:" + strings.Repeat("b", 64)
	for _, mode := range []string{"accepted", "host failure", "recoverable error", "missing RRO", "restarted", "missing exit", "missing restarts", "wrong image", "Pod replaced", "node replaced", "node rebooted", "cluster replaced", "mutated command", "lifecycle", "startupProbe", "livenessProbe", "readinessProbe", "cleanup replacement", "cancel", "unjoined"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestDir(t)
			nodes := nativeTopologyNodes(count)
			for i := range nodes {
				nodes[i].Metadata.UID = fmt.Sprintf("node-uid-%d", i)
				nodes[i].Status.NodeInfo.BootID = fmt.Sprintf("boot-id-%d", i)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var namespace string
			deleted, cleanupObserved := false, false
			pods := map[string]map[string]any{}
			creates, logReads := 0, 0
			original := errors.New("original observation failure")
			capture := func(callCtx context.Context, args []string, phase string, budget time.Duration) ([]byte, error) {
				if len(args) < 5 || args[0] != "kubectl" || args[1] != "--kubeconfig" || args[2] != "tenant.config" || budget <= 0 {
					t.Fatalf("invalid command boundary: %v", args)
				}
				marshal := func(value any) ([]byte, error) { return json.Marshal(value) }
				switch {
				case strings.HasSuffix(phase, "before-nodes"), strings.HasSuffix(phase, "after-nodes"):
					copyNodes := append([]tenantNode(nil), nodes...)
					if strings.HasSuffix(phase, "after-nodes") {
						if mode == "node replaced" {
							copyNodes[0].Metadata.UID = "new-node-uid"
						}
						if mode == "node rebooted" {
							copyNodes[0].Status.NodeInfo.BootID = "new-boot-id"
						}
					}
					return marshal(map[string]any{"items": copyNodes})
				case strings.HasSuffix(phase, "before-cluster"), strings.HasSuffix(phase, "after-cluster"):
					uid := "tenant-cluster-uid"
					if mode == "cluster replaced" && strings.HasSuffix(phase, "after-cluster") {
						uid = "new-cluster-uid"
					}
					return marshal(map[string]any{"metadata": map[string]string{"name": "kube-system", "uid": uid}})
				case strings.HasSuffix(phase, "namespace-create"):
					raw, err := os.ReadFile(args[6])
					if err != nil {
						t.Fatal(err)
					}
					var manifest struct{ Metadata nativeCloudInitMetadata }
					if err := json.Unmarshal(raw, &manifest); err != nil {
						t.Fatal(err)
					}
					namespace = manifest.Metadata.Name
					return marshal(map[string]any{"metadata": map[string]string{"name": namespace, "uid": "namespace-uid"}})
				case strings.HasSuffix(phase, "namespace-cleanup-get"):
					cleanupObserved = true
					if callCtx.Err() != nil {
						t.Fatal("cleanup inherited canceled execution context")
					}
					if deleted {
						return nil, nil
					}
					uid := "namespace-uid"
					if mode == "cleanup replacement" {
						uid = "foreign-namespace-uid"
					}
					return marshal(map[string]any{"metadata": map[string]string{"name": namespace, "uid": uid}})
				case strings.HasSuffix(phase, "namespace-cleanup-delete"):
					raw, err := os.ReadFile(strings.TrimPrefix(args[6], "--filename="))
					if err != nil {
						t.Fatal(err)
					}
					var options struct{ Preconditions struct{ UID string } }
					if err := json.Unmarshal(raw, &options); err != nil || options.Preconditions.UID != "namespace-uid" {
						t.Fatal("namespace deletion lost exact UID", err)
					}
					if mode == "cleanup replacement" {
						t.Fatal("deleted substituted namespace")
					}
					deleted = true
					return nil, nil
				case strings.HasSuffix(phase, "-create"):
					creates++
					raw, err := os.ReadFile(args[6])
					if err != nil {
						t.Fatal(err)
					}
					var pod map[string]any
					if err := json.Unmarshal(raw, &pod); err != nil {
						t.Fatal(err)
					}
					// The host root is read-only inside a chroot, not a host
					// systemd session. --wait retries unavailable systemctl
					// calls forever even after cloud-init has completed.
					command := pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["command"].([]any)
					for _, arg := range command {
						if arg == "--wait" {
							t.Fatal("post-readiness cloud-init observation must not wait for chroot systemd")
						}
					}
					metadata := pod["metadata"].(map[string]any)
					name := metadata["name"].(string)
					metadata["uid"] = "pod-uid-" + name
					if mode == "mutated command" {
						pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["command"] = []string{"/bin/true"}
					}
					if mode == "lifecycle" || strings.HasSuffix(mode, "Probe") {
						hook := map[string]any{"exec": map[string]any{"command": []string{"/bin/true"}}}
						if mode == "lifecycle" {
							hook = map[string]any{"postStart": hook}
						}
						pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)[mode] = hook
					}
					pods[name] = pod
					return marshal(pod)
				case strings.HasSuffix(phase, "-observe"), strings.HasSuffix(phase, "-after-stdout"):
					if mode == "cancel" {
						cancel()
						return nil, errors.Join(original, ctx.Err())
					}
					if mode == "unjoined" {
						return nil, errors.Join(original, ErrUnjoined)
					}
					pod := pods[args[6]]
					if mode == "Pod replaced" {
						pod["metadata"].(map[string]any)["uid"] = "new-pod-uid"
					}
					image, mount, restarts, exit, phase := image, "Enabled", 0, 0, "Succeeded"
					if mode == "wrong image" {
						image = "sha256:other"
					}
					if mode == "missing RRO" {
						mount = "Disabled"
					}
					if mode == "restarted" {
						restarts = 1
					}
					if mode == "host failure" || mode == "recoverable error" {
						exit, phase = 1, "Failed"
					}
					pod["status"] = map[string]any{"phase": phase, "containerStatuses": []any{map[string]any{"name": "status", "imageID": image, "containerID": "containerd://owned", "restartCount": restarts, "state": map[string]any{"terminated": map[string]int{"exitCode": exit}}, "volumeMounts": []any{map[string]string{"name": "host", "mountPath": "/host", "recursiveReadOnly": mount}}}}}
					status := pod["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)
					if mode == "missing exit" {
						status["state"] = map[string]any{"terminated": map[string]any{}}
					}
					if mode == "missing restarts" {
						delete(status, "restartCount")
					}
					return marshal(pod)
				case strings.HasSuffix(phase, "-stdout"):
					logReads++
					return []byte(cleanNativeCloudInit), nil
				}
				t.Fatalf("unexpected command: %s %v", phase, args)
				return nil, nil
			}
			err := runNativeCloudInit(ctx, root, "tenant.config", "kubectl", "1.34.2", image, count, nodes, capture)
			if (err == nil) != (mode == "accepted") {
				t.Fatalf("observation result=%v", err)
			}
			if mode == "accepted" && (creates != count || logReads != count || !deleted) {
				t.Fatalf("lost zero-retry all-node observation: creates=%d logs=%d deleted=%v", creates, logReads, deleted)
			}
			if mode == "unjoined" {
				if cleanupObserved || !errors.Is(err, ErrUnjoined) {
					t.Fatal("unjoined command must retain diagnostic namespace", err)
				}
			} else if !cleanupObserved || !deleted && mode != "cleanup replacement" {
				t.Fatal("did not join exact owned namespace cleanup", err)
			}
			if mode == "cancel" && (!errors.Is(err, original) || !errors.Is(err, context.Canceled)) {
				t.Fatal("lost original cancellation cause", err)
			}
			if mode == "accepted" {
				for i := range nodes {
					name := fmt.Sprintf("status-%d", i)
					raw, readErr := os.ReadFile(filepath.Join(root, "cloud-init-"+name+"-stdout.json"))
					if readErr != nil || string(raw) != cleanNativeCloudInit {
						t.Fatal("original host stdout was not preserved", readErr)
					}
				}
			}
		})
	}
}

// The existing owned subprocess fixture delegates only the new diagnostic
// commands here. Standard JSON decoding keeps the fixture independent of shell
// text parsing; it does not execute a container, guest or cloud-init command.
func TestNativeCloudInitFixtureTool(t *testing.T) {
	if os.Getenv("NVKR_TEST_CLOUD_INIT_TOOL") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	root := os.Getenv("FIXTURE")
	emit := func(value any) {
		if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	switch args[0] {
	case "create":
		raw, err := os.ReadFile(args[2])
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		metadata := object["metadata"].(map[string]any)
		name := metadata["name"].(string)
		metadata["uid"] = "fixture-" + name
		if object["kind"] == "Pod" {
			image := object["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"].(string)
			object["status"] = map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "status", "imageID": image, "containerID": "containerd://fixture", "restartCount": 0, "state": map[string]any{"terminated": map[string]int{"exitCode": 0}}, "volumeMounts": []any{map[string]string{"name": "host", "mountPath": "/host", "recursiveReadOnly": "Enabled"}}}}}
			if err := privateJSON(filepath.Join(root, "cloud-init-tool-"+name+".json"), object); err != nil {
				t.Fatal(err)
			}
		}
		emit(object)
	case "get":
		if args[1] == "pod" {
			raw, err := os.ReadFile(filepath.Join(root, "cloud-init-tool-"+args[2]+".json"))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = os.Stdout.Write(raw)
		} else if _, err := os.Stat(filepath.Join(root, "cloud-init-tool-deleted")); args[2] == "kube-system" || os.IsNotExist(err) {
			emit(map[string]any{"metadata": map[string]string{"name": args[2], "uid": "fixture-" + args[2]}})
		}
	case "logs":
		file, err := os.OpenFile(filepath.Join(root, "events"), os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintln(file, "cloud-init-"+args[1])
		if err := errors.Join(err, file.Close()); err != nil {
			t.Fatal(err)
		}
		fmt.Print(cleanNativeCloudInit)
	case "delete":
		if err := privateWrite(filepath.Join(root, "cloud-init-tool-deleted"), []byte("deleted")); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unexpected fixture command %v", args)
	}
	os.Exit(0)
}
