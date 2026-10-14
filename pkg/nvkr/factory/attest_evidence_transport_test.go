// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
)

// Exercise the generated stage's ownership handoff and outgoing carrier with
// synthetic signer evidence. This does not exercise GitHub or cryptography.
func TestAttestedPublicationTransportConsumesPreparedInput(t *testing.T) {
	plan, artifacts, _ := referenceMetadataFixture(t)
	nodes, _ := planIndex(plan)
	for _, output := range plan.LifecycleOutputs {
		t.Run(output.Name, func(t *testing.T) {
			node := nodes[NodeAttestEvidence+"/"+outputPublicationRoute(output.Name)]
			input, err := signNodeInput(node, artifacts, PackageOCI)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(input.Path); !os.IsNotExist(err) {
				t.Fatalf("prepared input remains after attestation: %v", err)
			}
			final, err := signNodeOutput(node, artifacts, PackageOCI)
			if err != nil {
				t.Fatal(err)
			}
			before, err := delivery.Inspect(t.Context(), final.Path)
			if err != nil {
				t.Fatal(err)
			}
			if err := PackGitHubOutputs(t.Context(), plan, node.ID, GitHubStepOptions{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			if err := transportarchive.Unpack(t.Context(), carrierPath(final.ID), destination, []string{final.Path}); err != nil {
				t.Fatal(err)
			}
			after, err := delivery.Inspect(t.Context(), filepath.Join(destination, final.Path))
			if err != nil {
				t.Fatal(err)
			}
			if before.RootDigest != after.RootDigest {
				t.Fatal("transport changed the attested publication identity")
			}
			if err := delivery.RequireAttestedEvidence(after); err != nil {
				t.Fatal(err)
			}
		})
	}
}
