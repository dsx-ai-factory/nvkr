// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPullOCIBlobTransportAdmission(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, artifact, output string
		code                   e.Code
	}{
		{"blob", "oci-blob://registry.example/charts/tool@" + digest, "raw", ""},
		{"loopback registry", "oci-blob://localhost:5000/charts/tool@" + digest, "raw", ""},
		{"tag", "oci-blob://registry.example/charts/tool:1.2.3", "raw", e.CodeUnpinnedSource},
		{"tag with digest", "oci-blob://registry.example/charts/tool:1.2.3@" + digest, "raw", e.CodeUnpinnedSource},
		{"different digest", "oci-blob://registry.example/charts/tool@sha256:" + strings.Repeat("b", 64), "raw", e.CodeUnpinnedSource},
		{"implicit registry", "oci-blob://charts/tool@" + digest, "raw", e.CodeUnpinnedSource},
		{"query", "oci-blob://registry.example/charts/tool@" + digest + "?tag=latest", "raw", e.CodeUnpinnedSource},
		{"image confusion", "oci-blob://registry.example/charts/tool@" + digest, "oci-image", e.CodeInvalidCatalog},
		{"package confusion", "oci-blob://registry.example/charts/tool@" + digest, "host-package", e.CodeInvalidCatalog},
		{"unknown OCI scheme", "oci-chart://registry.example/charts/tool@" + digest, "raw", e.CodeInvalidCatalog},
		{"ambiguous OCI scheme", "oci://registry.example/charts/tool@" + digest, "raw", e.CodeInvalidCatalog},
		{"unsupported scheme", "file:///tmp/chart.tgz", "raw", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := validRawPullNode()
			node.Artifact, node.OutputType = tc.artifact, tc.output
			err := node.Validate()
			if tc.code != "" {
				assertIRCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPullOCIBlobTransportIsExcludedFromBuildIdentity(t *testing.T) {
	node := validRawPullNode()
	node.Artifact = "oci-blob://registry.example/charts/tool@" + node.Digest
	graph := &Graph{SchemaVersion: SchemaVersion, Platform: node.Platform, Nodes: []Node{
		node, {ID: "verify", Kind: KindVerify, Needs: []string{node.ID}},
	}}
	before, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []string{
		"oci-blob://mirror.example/charts/tool@" + node.Digest,
		"https://mirror.example/charts/tool.tgz",
	} {
		graph.Nodes[0].Artifact = artifact
		after, err := graph.Digest()
		if err != nil || before != after {
			t.Fatalf("transport changed logical byte identity: %s, %s, %v", before, after, err)
		}
	}
	graph.Nodes[0].Digest = "sha256:" + strings.Repeat("b", 64)
	after, err := graph.Digest()
	if err != nil || after == before {
		t.Fatalf("new content pin did not change build identity: %s, %s, %v", before, after, err)
	}
}
