//go:build integration

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tektonkind

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/tekton"
	"sigs.k8s.io/yaml"
)

func (l *lab) result(ctx context.Context, name, image string) []byte {
	var claims struct {
		Items []struct {
			Metadata struct {
				Name            string
				OwnerReferences []struct{ Name string }
			}
		}
	}
	must(l.t, json.Unmarshal(l.kube(ctx, nil, "-n", "nvkr", "get", "pvc", "-o", "json"), &claims))
	claim := ""
	for _, item := range claims.Items {
		if len(item.Metadata.OwnerReferences) == 1 && item.Metadata.OwnerReferences[0].Name == name {
			if claim != "" {
				l.t.Fatal("ambiguous Run PVC")
			}
			claim = item.Metadata.Name
		}
	}
	if claim == "" {
		l.t.Fatal("missing Run PVC")
	}
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]string{"name": "carrier-reader", "namespace": "nvkr"}, "spec": map[string]any{"automountServiceAccountToken": false, "restartPolicy": "Never", "containers": []any{map[string]any{"name": "reader", "image": image, "command": []string{"/bin/sleep", "3600"}, "volumeMounts": []any{map[string]any{"name": "carriers", "mountPath": "/carriers", "readOnly": true}}}}, "volumes": []any{map[string]any{"name": "carriers", "persistentVolumeClaim": map[string]any{"claimName": claim, "readOnly": true}}}}}
	raw, err := json.Marshal(pod)
	must(l.t, err)
	l.kube(ctx, raw, "create", "-f", "-")
	l.kube(ctx, nil, "-n", "nvkr", "wait", "--for=condition=Ready", "pod/carrier-reader", "--timeout=120s")
	archive := l.kube(ctx, nil, "-n", "nvkr", "exec", "carrier-reader", "--", "cat", "/carriers/nvkr-finalize/carrier-0000.tar")
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		must(l.t, err)
		if header.Name == ".nvkr/tekton/result/manifest.json" {
			raw, err := io.ReadAll(io.LimitReader(reader, 4<<20))
			must(l.t, err)
			return raw
		}
	}
	l.t.Fatal("final carrier omits release manifest")
	return nil
}

func (l *lab) provider(ctx context.Context, name, uid string, raw []byte, digest string) *tekton.Provider {
	var expected struct{ Spec json.RawMessage }
	must(l.t, json.Unmarshal(raw, &expected))
	var config struct {
		Clusters []struct {
			Cluster struct {
				Server string
				CA     string `json:"certificate-authority-data"`
			}
		}
	}
	must(l.t, yaml.Unmarshal(read(l.t, l.kubeconfig), &config))
	if len(config.Clusters) != 1 {
		l.t.Fatal("kind kubeconfig must contain exactly the owned cluster")
	}
	ca, err := base64.StdEncoding.DecodeString(config.Clusters[0].Cluster.CA)
	must(l.t, err)
	// This token is used only in memory and never enters retained diagnostics.
	token := strings.TrimSpace(string(l.kube(ctx, nil, "-n", "nvkr", "create", "token", "nvkr-finalizer", "--duration=10m")))
	provider, err := tekton.NewProvider(tekton.ProviderPolicy{Server: config.Clusters[0].Cluster.Server, Namespace: "nvkr", RunName: name, RunUID: uid, PlanDigest: digest, RunSpec: expected.Spec, CertificateAuthority: ca, Token: token})
	must(l.t, err)
	return provider
}

func taskSpec(t *testing.T, raw []byte, stage string) json.RawMessage {
	var run struct {
		Spec struct {
			PipelineSpec struct {
				Tasks, Finally []struct {
					Name     string
					TaskSpec json.RawMessage
				}
			}
		}
	}
	must(t, json.Unmarshal(raw, &run))
	for _, task := range append(run.Spec.PipelineSpec.Tasks, run.Spec.PipelineSpec.Finally...) {
		if task.Name == stage {
			spec, err := tekton.EmbeddedTaskSpec(task.TaskSpec)
			must(t, err)
			return spec
		}
	}
	t.Fatalf("no task %s", stage)
	return nil
}

func (l *lab) checkCarriers(ctx context.Context, name, uid string, run []byte, digest, image string) {
	provider := l.provider(ctx, name, uid, run, digest)
	defer provider.Close()
	receipt, err := provider.AuthenticateStage(ctx, "nvkr-finalize", "nvkr-finalizer", taskSpec(l.t, run, "nvkr-finalize"))
	must(l.t, err)
	raw := l.kube(ctx, nil, "-n", "nvkr", "exec", "carrier-reader", "--", "cat", "/carriers/nvkr-finalize/manifest.json")
	archive := l.kube(ctx, nil, "-n", "nvkr", "exec", "carrier-reader", "--", "cat", "/carriers/nvkr-finalize/carrier-0000.tar")
	set, err := tekton.AuthenticateCarriers(ctx, receipt, raw, map[string]string{"result": ".nvkr/tekton/result"})
	must(l.t, err)
	for _, fault := range []string{"complete", "omitted", "corrupted", "substituted manifest"} {
		l.t.Run(fault, func(t *testing.T) {
			directory, workspace := t.TempDir(), t.TempDir()
			payload := bytes.Clone(archive)
			if fault == "corrupted" {
				payload[len(payload)/2] ^= 1
			}
			if fault != "omitted" {
				must(t, os.WriteFile(filepath.Join(directory, tekton.CarrierFile(0)), payload, 0600))
			}
			if fault == "substituted manifest" {
				altered := bytes.Clone(raw)
				altered[len(altered)/2] ^= 1
				_, err := tekton.AuthenticateCarriers(ctx, receipt, altered, map[string]string{"result": ".nvkr/tekton/result"})
				requireCodedFailure(t, err)
				return
			}
			err := set.Restore(ctx, directory, workspace, []string{"result"})
			if fault == "complete" {
				must(t, err)
				_ = read(t, filepath.Join(workspace, ".nvkr/tekton/result/manifest.json"))
				return
			}
			requireCodedFailure(t, err)
			if _, err := os.Stat(filepath.Join(workspace, ".nvkr/tekton/result/manifest.json")); !os.IsNotExist(err) {
				t.Fatal("invalid carrier exposed a release manifest")
			}
		})
	}
}

func (l *lab) checkTerminalFailure(ctx context.Context, raw []byte) {
	var original map[string]any
	must(l.t, json.Unmarshal(raw, &original))
	digest := original["metadata"].(map[string]any)["annotations"].(map[string]any)[tekton.PlanAnnotation].(string)
	for _, fault := range []string{"failed prepare", "canceled Run"} {
		l.t.Run(fault, func(t *testing.T) {
			child := *l
			child.t = t
			l := &child
			var run map[string]any
			must(t, json.Unmarshal(raw, &run))
			if fault == "failed prepare" {
				spec := run["spec"].(map[string]any)["pipelineSpec"].(map[string]any)["tasks"].([]any)[0].(map[string]any)["taskSpec"].(map[string]any)
				step := spec["steps"].([]any)[0].(map[string]any)
				step["command"] = []string{"/bin/false"}
				// The API omits empty args; freeze the same exact specification.
				delete(step, "args")
			}
			exact, err := json.Marshal(run)
			must(t, err)
			created := l.kube(ctx, exact, "create", "-f", "-", "-o", "json")
			var identity struct{ Metadata struct{ Name, UID string } }
			must(t, json.Unmarshal(created, &identity))
			if fault == "canceled Run" {
				l.kube(ctx, nil, "-n", "nvkr", "patch", "pipelinerun", identity.Metadata.Name, "--type=merge", "-p", `{"spec":{"status":"Cancelled"}}`)
			}
			l.waitRun(ctx, identity.Metadata.Name, false)
			provider := l.provider(ctx, identity.Metadata.Name, identity.Metadata.UID, exact, digest)
			defer provider.Close()
			receipt, err := provider.AuthenticateStage(ctx, "nvkr-prepare", "nvkr-builder", taskSpec(t, exact, "nvkr-prepare"))
			if fault == "canceled Run" {
				requireCodedFailure(t, err)
				return
			}
			must(t, err)
			if receipt.State() != "failure" {
				t.Fatal("failed prepare did not authenticate as failure")
			}
			_, err = tekton.AuthenticateCarriers(ctx, receipt, []byte("{}"), nil)
			requireCodedFailure(t, err)
		})
	}
}

func requireCodedFailure(t *testing.T, err error) {
	t.Helper()
	var coded *e.Error
	if err == nil || !errors.As(err, &coded) || coded.Code == "" {
		t.Fatalf("expected coded rejection, got %v", err)
	}
}
