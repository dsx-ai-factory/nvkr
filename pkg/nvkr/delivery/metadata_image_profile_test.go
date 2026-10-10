// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestInspectMetadataEnforcesDeclaredImageProfileWithoutPayloads(t *testing.T) {
	for _, media := range []types.MediaType{types.OCIManifestSchema1, types.DockerManifestSchema2} {
		for _, test := range []struct {
			name       string
			edit       func(*testing.T, *ociManifest, *[]byte)
			diagnostic string
		}{
			{"valid", nil, ""},
			{"nonempty artifact type", func(_ *testing.T, manifest *ociManifest, _ *[]byte) { manifest.ArtifactType = MediaDeliveryObject },
				"container image manifest has an invalid schema or layer count"},
			{"invalid rootfs", func(_ *testing.T, _ *ociManifest, config *[]byte) {
				*config = bytes.Replace(*config, []byte(`"type":"layers"`), []byte(`"type":"other"`), 1)
			}, "container image config has an invalid platform, rootfs or command"},
			{"config schema alias", func(_ *testing.T, _ *ociManifest, config *[]byte) {
				*config = bytes.Replace(*config, []byte(`"architecture"`), []byte(`"Architecture"`), 1)
			}, `image schema member "Architecture" must use exact spelling "architecture"`},
			{"unsupported layer media", func(_ *testing.T, manifest *ociManifest, _ *[]byte) {
				manifest.Layers[0].MediaType = "application/octet-stream"
			}, "container image layer has an invalid identity, size or media type"},
			{"diff IDs count mismatch", func(t *testing.T, _ *ociManifest, config *[]byte) {
				editMetadataImageConfig(t, config, func(config *v1.ConfigFile) { config.RootFS.DiffIDs = nil })
			}, "container image config has an invalid platform, rootfs or command"},
			{"missing entrypoint and command", func(t *testing.T, _ *ociManifest, config *[]byte) {
				editMetadataImageConfig(t, config, func(config *v1.ConfigFile) {
					config.Config.Entrypoint = nil
					config.Config.Cmd = nil
				})
			}, "container image config has an invalid platform, rootfs or command"},
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
					if !strings.Contains(err.Error(), test.diagnostic) {
						t.Fatalf("image profile rejected for the wrong reason: want %q, got %v", test.diagnostic, err)
					}
					if !reflect.DeepEqual(got, Inspection{}) {
						t.Fatalf("invalid image profile returned metadata inspection: %+v", got)
					}
				})
				if test.edit == nil {
					t.Run("push without payloads", func(t *testing.T) { pushMetadataImageProfileFixture(t, root, pin) })
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
					if !strings.Contains(err.Error(), test.diagnostic) {
						t.Fatalf("image profile rejected for the wrong reason: want %q, got %v", test.diagnostic, err)
					}
					if reference != "" || requests.Load() != 0 {
						t.Fatalf("invalid image metadata reached publication: reference=%q requests=%d", reference, requests.Load())
					}
				})
			})
		}
	}
}

func editMetadataImageConfig(t *testing.T, raw *[]byte, edit func(*v1.ConfigFile)) {
	t.Helper()
	var config v1.ConfigFile
	if err := json.Unmarshal(*raw, &config); err != nil {
		t.Fatal(err)
	}
	edit(&config)
	encoded, err := canonical.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	*raw = encoded
}

func pushMetadataImageProfileFixture(t *testing.T, root, pin string) {
	t.Helper()
	inspection, err := InspectMetadata(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	manifests := make(map[string][]byte)
	mediaTypes := make(map[string]string)
	payloadLayers := 0
	for _, object := range inspection.Root.Objects {
		raw, err := os.ReadFile(fixtureBlobPath(root, object.DescriptorDigest))
		if err != nil {
			t.Fatal(err)
		}
		var manifest ociManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		manifests[object.DescriptorDigest] = raw
		mediaTypes[object.DescriptorDigest] = manifest.MediaType
		if object.Output != nil {
			payloadLayers += len(manifest.Layers)
		}
	}
	if payloadLayers == 0 {
		t.Fatal("fixture has no image payload layers")
	}
	index, rootManifest := fixtureManifest(t, root)
	indexRaw, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile(fixtureBlobPath(root, index.Manifests[0].Digest))
	if err != nil {
		t.Fatal(err)
	}
	configRaw, err := os.ReadFile(fixtureBlobPath(root, rootManifest.Config.Digest))
	if err != nil {
		t.Fatal(err)
	}
	const releaseRepository = "product/releases"
	releasePath := "/v2/" + releaseRepository
	allowedManifests := map[string][]byte{
		releasePath + "/manifests/" + pin:                       indexRaw,
		releasePath + "/manifests/" + index.Manifests[0].Digest: manifestRaw,
	}
	maxBodyBytes := max(len(indexRaw), len(manifestRaw), len(configRaw))
	var unexpectedRequests atomic.Int64
	var requestMu sync.Mutex
	uploadPath := ""
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		defer requestMu.Unlock()
		body, readErr := io.ReadAll(io.LimitReader(r.Body, int64(maxBodyBytes+1)))
		expectedManifest, isManifest := allowedManifests[r.URL.Path]
		allowed := false
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/":
			allowed = r.URL.RawQuery == "" && len(body) == 0
		case isManifest:
			allowed = r.URL.RawQuery == "" && (((r.Method == http.MethodGet || r.Method == http.MethodHead) && len(body) == 0) ||
				(r.Method == http.MethodPut && bytes.Equal(body, expectedManifest)))
		case r.Method == http.MethodHead && r.URL.Path == releasePath+"/blobs/"+rootManifest.Config.Digest:
			allowed = r.URL.RawQuery == "" && len(body) == 0
		case r.Method == http.MethodPost && r.URL.Path == releasePath+"/blobs/uploads/":
			allowed = r.URL.RawQuery == "" && len(body) == 0 && uploadPath == ""
		case uploadPath != "" && r.URL.Path == uploadPath:
			allowed = (r.Method == http.MethodPatch && r.URL.RawQuery == "" && bytes.Equal(body, configRaw)) ||
				(r.Method == http.MethodPut && len(body) == 0 && r.URL.RawQuery == (url.Values{"digest": {rootManifest.Config.Digest}}).Encode())
		}
		if readErr != nil || !allowed {
			unexpectedRequests.Add(1)
			t.Errorf("unexpected metadata publication request: %s %s (read error: %v)", r.Method, r.URL.RequestURI(), readErr)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		handler.ServeHTTP(w, r)
		if r.Method == http.MethodPost {
			uploadPath = w.Header().Get("Location")
		}
	}))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/" + releaseRepository
	ref, err := name.NewDigest(repository+"@"+pin, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := publicationReferences(ref, inspection.Root, true)
	if err != nil {
		t.Fatal(err)
	}
	childPaths := make(map[string]string)
	requestMu.Lock()
	for _, object := range inspection.Root.Objects {
		child := refs[object.Name]
		path := "/v2/" + child.Context().RepositoryStr() + "/manifests/" + child.DigestStr()
		childPaths[object.Name] = path
		allowedManifests[path] = manifests[object.DescriptorDigest]
		maxBodyBytes = max(maxBodyBytes, len(manifests[object.DescriptorDigest]))
	}
	requestMu.Unlock()
	// Metadata publication resolves the already-published child manifests. Seed
	// only those exact manifests. The allowlist rejects artifact blob paths,
	// mounts and upload bodies; only the exact root config can be uploaded.
	for _, object := range inspection.Root.Objects {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
			server.URL+childPaths[object.Name], bytes.NewReader(manifests[object.DescriptorDigest]))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", mediaTypes[object.DescriptorDigest])
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("seed child manifest: %s", response.Status)
		}
	}
	options := RegistryOptions{Anonymous: true, PlainHTTP: true}
	reference, err := PushMetadata(t.Context(), root, repository, options)
	if err != nil || reference != repository+"@"+pin {
		t.Fatalf("push metadata without image payloads: reference=%q, error=%v", reference, err)
	}
	resolved, err := ResolveDigest(t.Context(), reference, options)
	if err != nil || resolved != pin {
		t.Fatalf("read back published metadata root: digest=%q, error=%v", resolved, err)
	}
	if unexpectedRequests.Load() != 0 {
		t.Fatalf("metadata publication made %d requests outside its metadata allowlist", unexpectedRequests.Load())
	}
}

// Build self-consistent wire fixtures below the validating constructors. Every
// changed image config/manifest, evidence subject and release identity is freshly
// bound, so rejection must concern the image profile, not stale bytes or membership.
func metadataImageProfileFixture(t *testing.T, media types.MediaType, edit func(*testing.T, *ociManifest, *[]byte)) (string, string) {
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
		edit(t, &manifest, &config)
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
		if _, err := os.Stat(fixtureBlobPath(root, layer.Digest)); !os.IsNotExist(err) {
			t.Fatalf("metadata fixture contains an image payload: %v", err)
		}
	}
	return root, pin
}
