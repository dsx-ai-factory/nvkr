// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestInspectReleaseLayoutReturnsExactSortedObjects(t *testing.T) {
	layout, digest := testPublicationLayout(t)
	inspection, err := delivery.Inspect(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	got, err := InspectReleaseLayout(t.Context(), "registry.example.test/product@"+digest, layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "tools" {
		t.Fatalf("wrong inspection scope: %+v", got)
	}
	if got[0].Reference != "registry.example.test/tools@"+got[0].Digest {
		t.Fatal("missing artifact repository reference", got)
	}
	for _, artifact := range got {
		found := false
		for _, object := range inspection.Root.Objects {
			if object.Name == artifact.Name {
				found = object.Output != nil && artifact.Kind == object.Output.Format && artifact.Digest == object.DescriptorDigest && artifact.BuildDigest == object.Output.BuildDigest
			}
		}
		if !found {
			t.Fatalf("inspection fabricated artifact binding: %+v", artifact)
		}
	}
	for _, tc := range []struct {
		name, ref, path string
		code            e.Code
	}{
		{"wrong digest", "registry.example.test/product@sha256:" + repeat("a", 64), layout, e.CodeVerifyFailed},
		{"unpinned reference", "registry.example.test/product:latest", layout, e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifacts, err := InspectReleaseLayout(t.Context(), tc.ref, tc.path)
			assertCode(t, err, tc.code)
			if artifacts != nil {
				t.Fatalf("failed inspection returned artifacts: %+v", artifacts)
			}
		})
	}
}

func TestConsumerMaterializationFailureCleansWorkspace(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid reference cleanup", true: "root is file"}[blocked], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "verification")
			if blocked {
				if err := os.WriteFile(root, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			consumer, err := NewConsumer(ConsumerOptions{WorkspaceRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			materialized, err := consumer.materialize(t.Context(), "invalid")
			if err == nil || materialized != nil {
				t.Fatalf("failed materialization returned state: %+v, %v", materialized, err)
			}
			if blocked {
				assertCode(t, err, e.CodeImmutableViolation)
				raw, err := os.ReadFile(root)
				if err != nil || string(raw) != "preserve" {
					t.Fatalf("existing root changed: %q, %v", raw, err)
				}
			} else {
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatalf("failed pull left staging: %v, %v", entries, err)
				}
			}
		})
	}
}

func TestPublicationCapabilitySurvivesSourceReplacement(t *testing.T) {
	layout, digest := testPublicationLayout(t)
	publication, err := delivery.OpenPublication(t.Context(), layout, digest)
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/product@" + digest
	outputs, subject, err := publication.Artifacts(reference)
	if err != nil {
		t.Fatal(err)
	}
	outputs[0].Name = "mutated"
	outputs[0].Components[0] = "mutated"
	entries, err := os.ReadDir(filepath.Join(layout, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.WriteFile(filepath.Join(layout, "blobs", "sha256", entry.Name()), []byte("substituted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(layout, "index.json"), []byte("substituted"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, againSubject, err := publication.Artifacts(reference)
	if err != nil || againSubject != subject || again[0].Name == "mutated" || again[0].Components[0] == "mutated" {
		t.Fatalf("held projection changed: %v %s %v", again, againSubject, err)
	}
	_, err = delivery.OpenPublication(t.Context(), layout, digest)
	assertCode(t, err, e.CodeVerifyFailed)
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = publication.Artifacts(reference)
	assertCode(t, err, e.CodeGateUnmet)
	assertCode(t, publication.RequireAttestedEvidence(), e.CodeGateUnmet)
}
