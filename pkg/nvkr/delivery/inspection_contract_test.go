// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func objectPublicationFixture(t *testing.T) (string, []byte) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "publication")
	payload := []byte(`{"schemaVersion":"example/v1","value":"exact"}`)
	_, err := BuildPublicationRoot(t.Context(), root, PublicationRequest{Objects: []ObjectInput{{Name: "record", Kind: "record", ArtifactType: MediaDeliveryObject, LayerMediaType: MediaOctetStream, SemanticDigest: digestBytes(payload), Bytes: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	return root, payload
}

func TestReadPublicationObjectReturnsDetachedExactPayload(t *testing.T) {
	root, want := objectPublicationFixture(t)
	object, got, err := ReadObject(t.Context(), root, "record")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) || object.BlobDigest != digestBytes(want) || object.Name != "record" {
		t.Fatalf("object = %+v, bytes = %q", object, got)
	}
	got[0] = '!'
	_, again, err := ReadObject(t.Context(), root, "record")
	if err != nil || !bytes.Equal(again, want) {
		t.Fatalf("caller mutated stored payload: %q, %v", again, err)
	}
	for _, name := range []string{"absent", ""} {
		_, _, err := ReadObject(t.Context(), root, name)
		assertRegistryCode(t, err, e.CodeUnverifiedRef)
		out := filepath.Join(t.TempDir(), "output")
		_, err = ExtractObject(t.Context(), root, name, out)
		assertRegistryCode(t, err, e.CodeUnverifiedRef)
		_, err = ExtractBinaryBundle(t.Context(), root, name, out)
		assertRegistryCode(t, err, e.CodeUnverifiedRef)
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("failed selection created output: %v", err)
		}
	}
}

func TestPublicationIndexRejectsSubstitutedBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ociIndex)
	}{
		{"wrong artifact type", func(i *ociIndex) { i.ArtifactType = MediaBinaryBundle }},
		{"wrong semantic digest", func(i *ociIndex) { i.Annotations["dev.nvkr.semantic.digest"] = digestBytes([]byte("other")) }},
		{"duplicate root", func(i *ociIndex) {
			for _, d := range i.Manifests {
				if d.ArtifactType == MediaPublicationRoot {
					i.Manifests = append(i.Manifests, d)
					break
				}
			}
		}},
		{"wrong object annotation", func(i *ociIndex) {
			for j := range i.Manifests {
				if i.Manifests[j].ArtifactType != MediaPublicationRoot {
					i.Manifests[j].Annotations["dev.nvkr.object.kind"] = "other"
				}
			}
		}},
		{"wrong descriptor size", func(i *ociIndex) { i.Manifests[0].Size++ }},
		{"unsupported descriptor media", func(i *ociIndex) { i.Manifests[0].MediaType = "application/unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := objectPublicationFixture(t)
			index, _, err := readIndex(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&index)
			raw, err := json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = Inspect(t.Context(), root)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			out := filepath.Join(t.TempDir(), "output")
			_, err = ExtractObject(t.Context(), root, "record", out)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("invalid publication exposed output: %v", err)
			}
		})
	}
}
