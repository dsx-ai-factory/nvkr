//go:build integration

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tektonkind

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// Compile the real maintained native selection without cluster or registry I/O.
// This is generation coverage only; TestKindCore owns external acceptance.
func TestTektonCoreProjection(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	l := &lab{t: t, root: filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))}
	must(t, json.Unmarshal(read(t, filepath.Join(l.root, "toolchains/tekton-kind/pins.json")), &l.pins))
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	keys := map[string]string{}
	for _, role := range []string{"publisher", "smoke", "qualifier"} {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		must(t, err)
		raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		must(t, err)
		keys[role] = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}))
	}
	plan := l.corePlan(t.Context(), l.pins.RunnerBase, ca, keys)
	bundle, err := factory.Render(t.Context(), plan, factory.EngineTekton)
	must(t, err)
	root := t.TempDir()
	must(t, factory.WriteGeneratedBundle(t.Context(), root, bundle))
	must(t, factory.CheckGenerated(t.Context(), plan, factory.EngineTekton, root))
	if len(plan.LifecycleOutputs) < 10 {
		t.Fatal("core selection was reduced to a trivial fixture")
	}
	if destination := os.Getenv("NVKR_TEKTON_PROJECTION"); destination != "" {
		must(t, os.WriteFile(destination, bundle.Files[bundle.WorkflowPath], 0600))
	}
}
