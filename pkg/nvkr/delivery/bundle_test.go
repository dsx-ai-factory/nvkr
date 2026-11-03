// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/registry"
)

func bundleArtifactFixture(t *testing.T) (string, string, []byte, Result) {
	t.Helper()
	raw := helmChartArchiveFixture(t, map[string]string{"manifests/application.yaml": "apiVersion: argoproj.io/v1alpha1\nkind: Application\n", "deploy.sh": "echo example\n"})
	archive := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(archive, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(t.TempDir(), "bundle")
	result, err := BuildBundleArtifact(t.Context(), layout, "runtime", archive, digestBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	return layout, archive, raw, result
}

func TestBuildBundleArtifactPreservesExactBytes(t *testing.T) {
	layout, archive, raw, built := bundleArtifactFixture(t)
	descriptor, got, err := ReadArtifact(t.Context(), layout)
	if err != nil || !bytes.Equal(got, raw) || descriptor.ArtifactType != MediaBundle || built.SemanticDigest != digestBytes(raw) {
		t.Fatalf("bundle bytes or identity changed: %+v %v", descriptor, err)
	}
	_, manifest := fixtureManifest(t, layout)
	if manifest.Config.MediaType != MediaEmptyConfig || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != MediaBundleLayer {
		t.Fatalf("bundle acquired an image/Helm profile: %+v", manifest)
	}
	other, err := BuildBundleArtifact(t.Context(), filepath.Join(t.TempDir(), "second"), "other-name", archive, digestBytes(raw))
	if err != nil || other.RootDigest != built.RootDigest {
		t.Fatalf("bundle transport is unstable: %+v %v", other, err)
	}
	_, err = InspectContainerImage(t.Context(), layout, "linux/arm64")
	assertRegistryCode(t, err, e.CodeVerifyFailed)
}

func TestBuildBundleArtifactAdmissionBeforeOutput(t *testing.T) {
	_, archive, raw, _ := bundleArtifactFixture(t)
	for _, test := range []struct {
		name, pin         string
		corrupt, canceled bool
		code              e.Code
	}{
		{"wrong pin", digestBytes([]byte("wrong")), false, false, e.CodeVerifyFailed},
		{"missing pin", "", false, false, e.CodeVerifyFailed},
		{"invalid archive", "", true, false, e.CodeVerifyFailed},
		{"canceled", digestBytes(raw), false, true, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, pin := archive, test.pin
			if test.corrupt {
				input = filepath.Join(t.TempDir(), "bad.gz")
				if err := os.WriteFile(input, []byte("not a bundle"), 0o600); err != nil {
					t.Fatal(err)
				}
				pin = digestBytes([]byte("not a bundle"))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			parent := t.TempDir()
			_, err := BuildBundleArtifact(ctx, filepath.Join(parent, "nested", "output"), "runtime", input, pin)
			assertRegistryCode(t, err, test.code)
			assertEmptyPullDirectory(t, parent)
		})
	}
}

func bundlePublicationRequest(t *testing.T) (PublicationRequest, []byte) {
	t.Helper()
	layout, archive, raw, built := bundleArtifactFixture(t)
	descriptor, err := PayloadDescriptor(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	build, payload := digestBytes([]byte("bundle build")), digestBytes(raw)
	content, err := ContentDigest([]Output{{Name: "runtime", Kind: outputFormatBundle, Digest: descriptor.Digest, BuildDigest: build, Components: []string{"producer"}}})
	if err != nil {
		t.Fatal(err)
	}
	request := PublicationRequest{Release: &ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build, ContentSubject: content}, Objects: []ObjectInput{{
		Name: "runtime", Kind: outputFormatBundle, Path: layout, Layout: true, ArtifactType: MediaBundle, SemanticDigest: payload, BlobDigest: built.RootDigest,
		OS: "linux", Arch: "arm64", Output: &OutputBinding{Repository: "runtime", Format: outputFormatBundle, BuildDigest: build, Components: []string{"producer"},
			BuildSubjects: []BuildSubject{{Component: "producer", Digest: payload}}},
	}}}
	inventory, err := sbom.InspectDependencies(t.Context(), archive, payload, "bundle-archive", sbom.Scanner{})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []struct{ component, stage, kind, subject, input string }{
		{"producer", "source-build", "source-archive", payload, ""},
		{"runtime", "packaging", "bundle", descriptor.Digest, payload},
	} {
		bom, err := sbom.NewSubject(build, stage.component, stage.subject)
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.WithDependencies(bom, inventory.Components, inventory.Method, "")
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.ForSubject(bom, stage.subject, stage.stage, stage.input)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		request.Objects = append(request.Objects, ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json",
			LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: encoded,
			Evidence: &EvidenceBinding{Outputs: []string{"runtime"}, Component: stage.component, Stage: stage.stage, SubjectKind: stage.kind, SubjectDigest: stage.subject, InspectionInputDigest: stage.input}})
	}
	return request, raw
}

func TestBundlePublicationAndSelectedRegistryPull(t *testing.T) {
	for _, scenario := range []string{"exact", "corrupt payload", "existing destination"} {
		t.Run(scenario, func(t *testing.T) {
			request, raw := bundlePublicationRequest(t)
			root := filepath.Join(t.TempDir(), "release")
			result, err := BuildPublicationRoot(t.Context(), root, request)
			if err != nil {
				t.Fatal(err)
			}
			publication, err := OpenPublication(t.Context(), root, result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			outputs, content, err := publication.Artifacts("registry.example/product/releases@" + result.RootDigest)
			if err != nil || len(outputs) != 1 || outputs[0].Kind != outputFormatBundle || outputs[0].Arch != "arm64" || content != request.Release.ContentSubject {
				t.Fatalf("wrong full description: %+v %s %v", outputs, content, err)
			}
			if err := publication.Close(); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(t.TempDir(), "metadata")
			if _, err := ExportMetadata(t.Context(), root, metadata); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(fixtureBlobPath(metadata, digestBytes(raw))); !os.IsNotExist(err) {
				t.Fatalf("metadata retained payload: %v", err)
			}
			if _, err := InspectMetadata(t.Context(), metadata); err != nil {
				t.Fatal(err)
			}
			var active atomic.Bool
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if active.Load() && scenario == "corrupt payload" && strings.HasSuffix(r.URL.Path, "/blobs/"+digestBytes(raw)) && r.Method == http.MethodGet {
					bad := append([]byte(nil), raw...)
					bad[0] ^= 1
					_, _ = w.Write(bad)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{Anonymous: true, PlainHTTP: true}
			ref, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			active.Store(true)
			parent, workspace := t.TempDir(), t.TempDir()
			out := filepath.Join(parent, "output")
			if scenario == "existing destination" {
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			pulled, err := PullOutput(t.Context(), ref, "runtime", out, workspace, options)
			assertEmptyPullDirectory(t, workspace)
			if scenario != "exact" {
				code := e.CodeVerifyFailed
				if scenario == "existing destination" {
					code = e.CodeImmutableViolation
					assertEmptyPullDirectory(t, out)
				} else {
					assertEmptyPullDirectory(t, parent)
				}
				assertRegistryCode(t, err, code)
				return
			}
			if err != nil || len(pulled.Files) != 1 || pulled.Files[0].Path != "runtime.tar.gz" || pulled.Files[0].SHA256 != digestBytes(raw) || pulled.Output.ChartVersion != "" {
				t.Fatalf("wrong pull: %+v %v", pulled, err)
			}
			got, err := os.ReadFile(filepath.Join(out, "runtime.tar.gz"))
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("archive bytes changed: %v", err)
			}
		})
	}
}

func TestBundleReleaseRequiresBothExactEvidenceStages(t *testing.T) {
	for _, scenario := range []string{"missing source", "missing packaging", "wrong archive", "unexpected transport"} {
		t.Run(scenario, func(t *testing.T) {
			request, _ := bundlePublicationRequest(t)
			switch scenario {
			case "missing source":
				request.Objects = append(request.Objects[:1], request.Objects[2:]...)
			case "missing packaging":
				request.Objects = request.Objects[:2]
			case "wrong archive":
				request.Objects[0].Output.BuildSubjects[0].Digest = digestBytes([]byte("different archive"))
			case "unexpected transport":
				request.Objects[0].Output.TransportPath = "bundle"
			}
			out := filepath.Join(t.TempDir(), "release")
			_, err := BuildPublicationRoot(t.Context(), out, request)
			code := e.CodeVerifyFailed
			if strings.HasPrefix(scenario, "missing") {
				code = e.CodeProvenanceMissing
			}
			assertRegistryCode(t, err, code)
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("failed release left output: %v", err)
			}
		})
	}
}

func TestBundleMetadataAdmissionWithoutPayload(t *testing.T) {
	layout, _, raw, _ := bundleArtifactFixture(t)
	_, original := fixtureManifest(t, layout)
	if err := os.Remove(fixtureBlobPath(layout, digestBytes(raw))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*ociManifest, *PublishedObject)
	}{
		{"exact", nil},
		{"image config", func(m *ociManifest, _ *PublishedObject) { m.Config.MediaType = MediaImageConfig }},
		{"Helm layer", func(m *ociManifest, _ *PublishedObject) { m.Layers[0].MediaType = MediaHelmChartLayer }},
		{"missing layer", func(m *ociManifest, _ *PublishedObject) { m.Layers = nil }},
		{"extra layer", func(m *ociManifest, _ *PublishedObject) { m.Layers = append(m.Layers, m.Layers[0]) }},
		{"empty payload", func(m *ociManifest, _ *PublishedObject) { m.Layers[0].Size = 0 }},
		{"oversized payload", func(m *ociManifest, _ *PublishedObject) { m.Layers[0].Size = 1 << 30 }},
		{"subject", func(m *ociManifest, _ *PublishedObject) { m.Subject = &m.Config }},
		{"semantic substitution", func(_ *ociManifest, o *PublishedObject) { o.SemanticDigest = digestBytes([]byte("wrong")) }},
		{"format substitution", func(_ *ociManifest, o *PublishedObject) { o.Output.Format = outputFormatHelm }},
		{"transport path", func(_ *ociManifest, o *PublishedObject) { o.Output.TransportPath = "bundle" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := original
			manifest.Layers = append([]Descriptor(nil), original.Layers...)
			object := PublishedObject{Kind: outputFormatBundle, ArtifactType: MediaBundle, BlobDigest: digestBytes(raw), SemanticDigest: digestBytes(raw), Output: &OutputBinding{Format: outputFormatBundle}}
			if test.edit != nil {
				test.edit(&manifest, &object)
			}
			err := inspectBundleMetadata(t.Context(), layout, manifest, &object)
			if test.edit == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"case alias", bytes.Replace(encoded, []byte(`"artifactType"`), []byte(`"ArtifactType"`), 1)},
		{"unknown field", append([]byte(`{"unsupported":true,`), encoded[1:]...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertRegistryCode(t, validateBundleManifestDocument(test.raw), e.CodeVerifyFailed)
		})
	}
}

func TestBundleFullInspectionRejectsInvalidArchive(t *testing.T) {
	for _, invalid := range [][]byte{[]byte("not gzip"), helmChartArchiveFixture(t, map[string]string{"../escape": "bad"})} {
		root := t.TempDir()
		writer := layoutWriter{root: root, plain: true}
		descriptor, _, err := writer.addBytesObject(t.Context(), ObjectInput{Name: "runtime", Kind: outputFormatBundle, ArtifactType: MediaBundle,
			LayerMediaType: MediaBundleLayer, Bytes: invalid, SemanticDigest: digestBytes(invalid)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, ArtifactType: MediaBundle, Manifests: []Descriptor{descriptor}}); err != nil {
			t.Fatal(err)
		}
		_, _, err = ReadArtifact(t.Context(), root)
		assertRegistryCode(t, err, e.CodeVerifyFailed)
	}
}
