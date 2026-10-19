// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPublicationExecutorPersistsExactRegistryIdentity(t *testing.T) {
	t.Setenv("NVKR_REGISTRY_USER", "")
	t.Setenv("NVKR_CREDENTIAL", "")
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	dir := t.TempDir()
	layout, output := filepath.Join(dir, "layout"), filepath.Join(dir, "published.ref")
	payload := []byte("published object bytes")
	semantic := "sha256:" + strings.Repeat("a", 64)
	result, err := delivery.BuildPublicationRoot(t.Context(), layout, delivery.PublicationRequest{Objects: []delivery.ObjectInput{{Name: "record", Kind: "record", Bytes: payload, SemanticDigest: semantic}}})
	if err != nil {
		t.Fatal(err)
	}
	repository := strings.TrimPrefix(server.URL, "http://") + "/fixture/publication"
	plan := &Plan{Targets: map[string]Target{"registry": {Kind: TargetOCI, Ref: repository + ":run", Anonymous: true, PlainHTTP: true}}}
	node := Node{ID: "publish/record", Target: "registry", Inputs: []string{"input"}, Outputs: []string{"result"}}
	artifacts := map[string]Artifact{"input": {Path: layout}, "result": {Path: output}}
	var outputLog bytes.Buffer
	if err := executePublish(t.Context(), plan, node, artifacts, &outputLog); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil || string(raw) != repository+"@"+result.RootDigest+"\n" {
		t.Fatalf("published identity = %q, %v", raw, err)
	}
	if !strings.Contains(outputLog.String(), strings.TrimSpace(string(raw))) {
		t.Fatalf("missing published identity log: %s", &outputLog)
	}
	pulled, err := delivery.Pull(t.Context(), strings.TrimSpace(string(raw)), filepath.Join(dir, "pulled"), delivery.RegistryOptions{Anonymous: true, PlainHTTP: true})
	if err != nil || pulled.RootDigest != result.RootDigest {
		t.Fatalf("published closure = %+v, %v", pulled, err)
	}
}

func TestPublicationExecutorRejectsBeforeOutput(t *testing.T) {
	t.Setenv("NVKR_REGISTRY_USER", "")
	t.Setenv("NVKR_CREDENTIAL", "")
	for _, tc := range []struct {
		name     string
		target   Target
		nonUnary bool
		code     e.Code
	}{
		{"non-unary", Target{Kind: TargetOCI}, true, e.CodePipelineDrift},
		{"unsupported adapter", Target{Kind: "unknown"}, false, e.CodeEngineCapability},
		{"missing credential", Target{Kind: TargetOCI, CredentialRef: "registry-secret"}, false, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "published.ref")
			plan := &Plan{Targets: map[string]Target{"registry": tc.target}}
			node := Node{ID: "publish/record", Target: "registry", Inputs: []string{"input"}, Outputs: []string{"result"}}
			if tc.nonUnary {
				node.Inputs = nil
			}
			err := executePublish(t.Context(), plan, node, map[string]Artifact{"input": {Path: "missing-layout"}, "result": {Path: output}}, io.Discard)
			assertFactoryErrorCode(t, err, tc.code)
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("failed publication wrote result: %v", err)
			}
		})
	}
}
