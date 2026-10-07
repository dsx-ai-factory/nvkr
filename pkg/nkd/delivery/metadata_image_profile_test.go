// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestInspectMetadataEnforcesDeclaredImageProfileWithoutPayloads(t *testing.T) {
	for _, media := range []types.MediaType{types.OCIManifestSchema1, types.DockerManifestSchema2} {
		for _, test := range []struct {
			name string
			edit func(*ociManifest, *[]byte)
		}{
			{"valid", nil},
			{"nonempty artifact type", func(manifest *ociManifest, _ *[]byte) { manifest.ArtifactType = MediaDeliveryObject }},
			{"invalid rootfs", func(_ *ociManifest, config *[]byte) {
				*config = bytes.Replace(*config, []byte(`"type":"layers"`), []byte(`"type":"other"`), 1)
			}},
			{"config schema alias", func(_ *ociManifest, config *[]byte) {
				*config = bytes.Replace(*config, []byte(`"architecture"`), []byte(`"Architecture"`), 1)
			}},
		} {
			t.Run(string(media)+"/"+test.name, func(t *testing.T) {
				root, pin := metadataImageProfileFixture(t, media, test.edit)
				t.Run("inspect", func(t *testing.T) {
					got, err := InspectMetadata(t.Context(), root)
					if test.edit == nil {
						if err != nil || got.RootDigest != pin || len(got.Root.Objects) != 2 {
							t.Fatalf("valid image metadata required payloads or changed identity: %+v, %v", got, err)
						}
						return
					}
					assertRegistryCode(t, err, e.CodeVerifyFailed)
					if !reflect.DeepEqual(got, Inspection{}) {
						t.Fatalf("invalid image profile returned metadata inspection: %+v", got)
					}
				})
				if test.edit == nil {
					return
				}
				t.Run("push rejects before network", func(t *testing.T) {
					var requests atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						requests.Add(1)
						w.WriteHeader(http.StatusForbidden)
					}))
					defer server.Close()
					reference, err := PushMetadata(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases",
						RegistryOptions{Anonymous: true, PlainHTTP: true})
					assertRegistryCode(t, err, e.CodeVerifyFailed)
					if reference != "" || requests.Load() != 0 {
						t.Fatalf("invalid image metadata reached publication: reference=%q requests=%d", reference, requests.Load())
					}
				})
			})
		}
	}
}

// Build self-consistent wire fixtures below the validating constructors. Every
// changed image config/manifest, evidence subject and release identity is freshly
// bound, so rejection must concern the image profile, not stale bytes or membership.
func metadataImageProfileFixture(t *testing.T, media types.MediaType, edit func(*ociManifest, *[]byte)) (string, string) {
	t.Helper()
	source := imageFixture(t)
	if media == types.DockerManifestSchema2 {
		source = nativeDockerLayerFixture(t, types.DockerLayer)
	}
	_, manifest := fixtureManifest(t, source)
	config, err := readBlob(t.Context(), source, manifest.Config, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(&manifest, &config)
	}
	root := filepath.Join(t.TempDir(), "metadata")
	writer := layoutWriter{root: root, plain: true}
	manifest.Config, err = writer.addBlobBytes(config, manifest.Config.MediaType)
	if err != nil {
		t.Fatal(err)
	}
	image, err := writer.addJSONBlob(manifest, manifest.MediaType)
	if err != nil {
		t.Fatal(err)
	}
	build := digestBytes([]byte("image metadata profile fixture build"))
	output := Output{Name: "worker", Kind: "oci", Digest: image.Digest, BuildDigest: build, Components: []string{"worker"}}
	content, err := ContentDigest([]Output{output})
	if err != nil {
		t.Fatal(err)
	}
	bom, err := sbom.NewSubject(build, "worker", image.Digest)
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.ForSubject(bom, image.Digest, "build-output", image.Digest)
	if err != nil {
		t.Fatal(err)
	}
	document, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	evidenceName := "sbom-" + strings.TrimPrefix(semantic, "sha256:")
	evidence, blob, err := writer.addBytesObject(t.Context(), ObjectInput{
		Name: evidenceName, Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json",
		SemanticDigest: semantic, Bytes: document, subject: &image,
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := PublicationRoot{SchemaVersion: PublicationRootSchema, Qualification: QualificationDiagnostic,
		Release: &ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build,
			InvocationID: "metadata-fixture", ContentSubject: content},
		Objects: []PublishedObject{
			{Name: output.Name, Kind: "container-image", ArtifactType: string(media), DescriptorDigest: image.Digest, Size: image.Size,
				SemanticDigest: image.Digest, BlobDigest: image.Digest, OS: "linux", Arch: "amd64",
				Output: &OutputBinding{Repository: output.Name, Format: output.Kind, BuildDigest: build, Components: output.Components,
					BuildSubjects: []BuildSubject{{Component: "worker", Digest: image.Digest}}}},
			{Name: evidenceName, Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", DescriptorDigest: evidence.Digest, Size: evidence.Size,
				SemanticDigest: semantic, BlobDigest: blob,
				Evidence: &EvidenceBinding{DocumentDigest: digestBytes(document), Outputs: []string{output.Name}, Component: "worker",
					Stage: "build-output", SubjectKind: "oci-manifest", SubjectDigest: image.Digest}},
		}}
	pin, _, err := writer.writePublicationMetadata(publication, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseEvidence(t.Context(), root, publication); err != nil {
		t.Fatalf("fixture has inconsistent evidence membership: %v", err)
	}
	for _, layer := range manifest.Layers {
		if _, err := os.Stat(diskBlobPath(root, layer.Digest)); !os.IsNotExist(err) {
			t.Fatalf("metadata fixture contains an image payload: %v", err)
		}
	}
	return root, pin
}
