// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tekton

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestProviderClaimRequiresExactRunAndStorage(t *testing.T) {
	const uid = "11111111-1111-1111-1111-111111111111"
	const spec = `{"accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"1Gi"}},"storageClassName":"standard","volumeMode":"Filesystem"}`
	for _, tc := range []struct {
		name string
		edit func(map[string]any, map[string]any)
		code e.Code
	}{
		{"exact bound volume", func(map[string]any, map[string]any) {}, ""},
		{"foreign namespace", func(m, _ map[string]any) { m["namespace"] = "foreign" }, e.CodeVerifyFailed},
		{"foreign owner", func(m, _ map[string]any) {
			m["ownerReferences"].([]any)[0].(map[string]any)["uid"] = "22222222-2222-2222-2222-222222222222"
		}, e.CodeVerifyFailed},
		{"deleted", func(m, _ map[string]any) { m["deletionTimestamp"] = "2026-11-09T20:00:00Z" }, e.CodeVerifyFailed},
		{"unbound", func(_, s map[string]any) { delete(s, "volumeName") }, e.CodeVerifyFailed},
		{"different storage", func(_, s map[string]any) { s["storageClassName"] = "foreign" }, e.CodePipelineDrift},
		{"injected snapshot", func(_, s map[string]any) {
			s["dataSource"] = map[string]string{"kind": "PersistentVolumeClaim", "name": "foreign"}
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]any{"name": "carrier", "namespace": "fixture", "uid": "33333333-3333-3333-3333-333333333333",
				"ownerReferences": []any{map[string]any{"apiVersion": "tekton.dev/v1", "kind": "PipelineRun", "name": "run", "uid": uid, "controller": true}}}
			var actual map[string]any
			if err := json.Unmarshal([]byte(spec), &actual); err != nil {
				t.Fatal(err)
			}
			actual["volumeName"] = "owned-volume"
			tc.edit(metadata, actual)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/fixture/persistentvolumeclaims/carrier" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("claim read escaped its exact scope")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": metadata, "spec": actual})
			}))
			defer server.Close()
			provider, err := NewProvider(ProviderPolicy{Server: server.URL, Namespace: "fixture", RunName: "run", RunUID: uid,
				PlanDigest: "sha256:" + strings.Repeat("a", 64), RunSpec: json.RawMessage(`{}`), Token: "synthetic-token",
				CertificateAuthority: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})})
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			err = provider.authenticateClaim(t.Context(), "carrier", json.RawMessage(spec))
			if tc.code == "" && err != nil {
				t.Fatal(err)
			} else if tc.code != "" {
				requireCode(t, err, tc.code)
			}
		})
	}
}
