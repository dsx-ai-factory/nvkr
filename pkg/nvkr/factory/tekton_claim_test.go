// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/tekton"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

func TestTektonWorkspaceAuthenticatesAfterCoreCodec(t *testing.T) {
	base := tektonTestPlan(t)
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	scheme := k8sruntime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	codecs := serializer.NewCodecFactory(scheme)
	for _, tc := range []struct {
		name, storage, substitute string
		gib                       int
		code                      e.Code
	}{
		{name: "32 GiB", gib: 32, storage: "32Gi"},
		{name: "1024 GiB", gib: 1024, storage: "1Ti"},
		{name: "substituted capacity", gib: 1024, storage: "1Ti", substitute: "512Gi", code: e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := base.Environment.Clone()
			env.Tekton.WorkspaceGiB = tc.gib
			plan, err := Compile(t.Context(), base.Definition, env, stack, base.GeneratorVersion)
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := renderTekton(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			var resources struct{ Items []map[string]any }
			if err := json.Unmarshal(bundle.Files[tektonResourcePath], &resources); err != nil {
				t.Fatal(err)
			}
			var run map[string]any
			for _, item := range resources.Items {
				if item["kind"] == "PipelineRun" {
					run = item
				}
			}
			encode := func(value any) []byte {
				t.Helper()
				raw, err := canonical.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			const runUID = "11111111-1111-1111-1111-111111111111"
			const taskUID = "22222222-2222-2222-2222-222222222222"
			digest, err := plan.IdentityDigest()
			if err != nil {
				t.Fatal(err)
			}
			metadata := func(name, uid string) map[string]any {
				return map[string]any{"name": name, "uid": uid, "namespace": "nvkr",
					"annotations": map[string]string{tekton.PlanAnnotation: digest}}
			}
			owner := []map[string]any{{"apiVersion": "tekton.dev/v1", "kind": "PipelineRun", "name": "run", "uid": runUID, "controller": true}}
			run["metadata"] = metadata("run", runUID)
			run["status"] = map[string]any{"childReferences": []map[string]string{{"apiVersion": "tekton.dev/v1", "kind": "TaskRun", "name": "run-prepare", "pipelineTaskName": tektonPrepare}}}
			runSpec := run["spec"].(map[string]any)
			selected := runSpec["pipelineSpec"].(map[string]any)["tasks"].([]any)[0].(map[string]any)
			taskSpec, err := tekton.EmbeddedTaskSpec(encode(selected["taskSpec"]))
			if err != nil {
				t.Fatal(err)
			}
			var bindings []map[string]any
			for _, value := range selected["workspaces"].([]any) {
				binding := value.(map[string]any)
				entry := map[string]any{"name": binding["name"], "persistentVolumeClaim": map[string]string{"claimName": "carrier"}}
				if path, ok := binding["subPath"]; ok {
					entry["subPath"] = path
				}
				bindings = append(bindings, entry)
			}
			taskMetadata := metadata("run-prepare", taskUID)
			taskMetadata["ownerReferences"] = owner
			taskMetadata["labels"] = map[string]string{"tekton.dev/pipelineTask": tektonPrepare, "tekton.dev/pipelineRun": "run"}
			task := map[string]any{"apiVersion": "tekton.dev/v1", "kind": "TaskRun", "metadata": taskMetadata,
				"spec": map[string]any{"taskSpec": json.RawMessage(taskSpec), "serviceAccountName": "nvkr-builder", "timeout": selected["timeout"],
					"podTemplate": runSpec["taskRunTemplate"].(map[string]any)["podTemplate"], "workspaces": bindings,
					"params": []map[string]string{{"name": "run-name", "value": "run"}, {"name": "run-uid", "value": runUID}}},
				"status": map[string]any{"conditions": []map[string]string{{"type": "Succeeded", "status": "Unknown", "reason": "Running"}}}}
			claimMetadata := metadata("carrier", "33333333-3333-3333-3333-333333333333")
			claimMetadata["ownerReferences"] = owner
			claimSpec := runSpec["workspaces"].([]any)[0].(map[string]any)["volumeClaimTemplate"].(map[string]any)["spec"]
			claimRaw := encode(map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": claimMetadata, "spec": claimSpec})
			object, _, err := codecs.UniversalDeserializer().Decode(claimRaw, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			claim := object.(*corev1.PersistentVolumeClaim)
			claim.Spec.VolumeName = "owned-volume"
			if tc.substitute != "" {
				claim.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse(tc.substitute)
			}
			claimRaw, err = k8sruntime.Encode(codecs.LegacyCodec(corev1.SchemeGroupVersion), claim)
			if err != nil {
				t.Fatal(err)
			}
			documents := map[string][]byte{
				"/apis/tekton.dev/v1/namespaces/nvkr/pipelineruns/run":     encode(run),
				"/apis/tekton.dev/v1/namespaces/nvkr/taskruns/run-prepare": encode(task),
				"/api/v1/namespaces/nvkr/persistentvolumeclaims/carrier":   claimRaw,
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, ok := documents[r.URL.Path]
				if !ok || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("provider request escaped the exact fixture scope")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			provider, err := tekton.NewProvider(tekton.ProviderPolicy{Server: server.URL, Namespace: "nvkr", RunName: "run", RunUID: runUID,
				PlanDigest: digest, RunSpec: encode(runSpec), Token: "synthetic-token",
				CertificateAuthority: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})})
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			receipt, err := provider.AuthenticateExecution(t.Context(), tekton.TaskExpectation{Name: tektonPrepare, RunName: "run-prepare", RunUID: taskUID, ServiceAccount: "nvkr-builder", Spec: taskSpec})
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				if receipt != nil {
					t.Fatal("substituted storage issued an execution receipt")
				}
				return
			}
			if err != nil {
				t.Fatalf("core/v1 codec changed the rendered storage contract: %v", err)
			}
			if receipt.State() != "running" || claim.Spec.Resources.Requests.Storage().String() != tc.storage {
				t.Fatal("wrong execution state or canonical capacity")
			}
		})
	}
}
