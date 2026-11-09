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
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// This models the specific rejection reported in GitLab #533836, not a GitLab
// deployment or its version. All other Distribution behavior is the existing
// registry fixture. No credentials, provider configuration or live writes.
func artifactTypeRejectingRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") {
			raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			if err != nil {
				http.Error(w, "fixture cannot read manifest", http.StatusBadRequest)
				return
			}
			var document struct {
				ArtifactType string `json:"artifactType"`
			}
			if err := json.Unmarshal(raw, &document); err != nil {
				http.Error(w, "fixture cannot decode manifest", http.StatusBadRequest)
				return
			}
			if document.ArtifactType != "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"errors":[{"code":"MANIFEST_INVALID","message":"unknown media type"}]}`)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRegistryRejectsLegacyHelmArtifactType(t *testing.T) {
	server := artifactTypeRejectingRegistry(t)
	root, _ := helmMetadataLayoutFixture(t)
	_, manifest := fixtureManifest(t, root)
	manifest.ArtifactType = manifest.artifactType()
	delete(manifest.Annotations, artifactTypeAnnotation)
	descriptor, err := (layoutWriter{root: root}).addJSONBlob(manifest, MediaOCIManifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (layoutWriter{root: root}).writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}}); err != nil {
		t.Fatal(err)
	}
	ref, err := name.NewDigest(strings.TrimPrefix(server.URL, "http://")+"/chart@"+descriptor.Digest, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := v1.NewHash(descriptor.Digest)
	if err != nil {
		t.Fatal(err)
	}
	image, err := layout.Path(root).Image(hash)
	if err != nil {
		t.Fatal(err)
	}
	err = remote.Write(ref, image, registryOptions(t.Context(), RegistryOptions{PlainHTTP: true, Anonymous: true})...)
	if err == nil || !strings.Contains(err.Error(), "MANIFEST_INVALID") {
		t.Fatalf("legacy type did not reproduce the rejection: %v", err)
	}
}

// Reassemble existing checked synthetic fixture products through the public
// constructor, including every original evidence member. These are descriptive
// local transport fixtures, not completed producer Runs or signed releases.
func registryCompatibilityPublication(t *testing.T) (string, Result) {
	t.Helper()
	cli := filepath.Join(t.TempDir(), "cli-publication")
	if _, err := BuildPublicationRoot(t.Context(), cli, releasePublicationRequestWithRepresentation(t, "", "", OCINVKRCompatibleV1)); err != nil {
		t.Fatal(err)
	}
	chart, _ := helmPublicationWithRepresentation(t, "", "", nil, OCINVKRCompatibleV1)
	images, _, _ := registryImageIndexFixture(t)
	request := PublicationRequest{OCIRepresentation: OCINVKRCompatibleV1}
	var outputs []Output
	for _, root := range []string{cli, chart, images} {
		inspection, err := Inspect(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		if request.Release == nil {
			request.Release = inspection.Root.Release
		}
		part, err := MetadataOutputs(inspection.Root)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, part...)
		for _, object := range inspection.Root.Objects {
			input := ObjectInput{Name: object.Name, Kind: object.Kind, ArtifactType: object.ArtifactType,
				SemanticDigest: object.SemanticDigest, Output: object.Output, Evidence: object.Evidence,
				OS: object.OS, Arch: object.Arch}
			if object.Output != nil && object.Output.ImageIndex != nil {
				held, err := openPublishedImageIndex(t.Context(), root, object)
				if err != nil {
					t.Fatal(err)
				}
				input.Path, input.Layout = writeIndexFixture(t, held), true
				input.BlobDigest = object.DescriptorDigest
				if err := held.Close(); err != nil {
					t.Fatal(err)
				}
			} else if object.Output != nil {
				input.Path, input.Layout = t.TempDir(), true
				descriptor := Descriptor{MediaType: publishedManifestMediaType(object), ArtifactType: object.ArtifactType,
					Digest: object.DescriptorDigest, Size: object.Size}
				writer := layoutWriter{root: input.Path}
				if err := writer.copyClosure(t.Context(), root, descriptor); err != nil {
					t.Fatal(err)
				}
				pin, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}})
				if err != nil {
					t.Fatal(err)
				}
				input.BlobDigest = pin
			} else {
				_, input.Bytes, err = ReadObject(t.Context(), root, object.Name)
				if err != nil {
					t.Fatal(err)
				}
				input.LayerMediaType = object.ArtifactType
			}
			request.Objects = append(request.Objects, input)
		}
	}
	var err error
	request.Release.ContentSubject, err = ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	return root, result
}

func TestRegistryCompatibilityCompletePublication(t *testing.T) {
	server := artifactTypeRejectingRegistry(t)
	root, result := registryCompatibilityPublication(t)
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	repository := strings.TrimPrefix(server.URL, "http://") + "/product/releases"
	pinned, err := Push(t.Context(), root, repository, options)
	if err != nil {
		t.Fatal(err)
	}
	if pinned != repository+"@"+result.RootDigest {
		t.Fatalf("publication identity changed: %s", pinned)
	}
	before, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "pulled")
	after, err := Pull(t.Context(), pinned, destination, options)
	if err != nil {
		t.Fatal(err)
	}
	if after.RootDigest != before.RootDigest || !reflect.DeepEqual(after.Root, before.Root) {
		t.Fatal("registry round trip changed membership, payload or evidence binding")
	}
	if len(after.Root.Objects) != 7 {
		t.Fatalf("incomplete publication: %d objects", len(after.Root.Objects))
	}
	if _, err := PullMetadata(t.Context(), pinned, filepath.Join(t.TempDir(), "metadata"), options); err != nil {
		t.Fatal(err)
	}
	outputs, err := MetadataOutputs(before.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range outputs {
		if _, err := PullOutput(t.Context(), pinned, output.Name, filepath.Join(t.TempDir(), "selected"), t.TempDir(), options); err != nil {
			t.Fatalf("compatible output %q could not be pulled: %v", output.Name, err)
		}
	}
	if _, err := OpenPublication(t.Context(), destination, digestBytes([]byte("stale root"))); err == nil {
		t.Fatal("stale identity accepted")
	} else {
		assertRegistryCode(t, err, e.CodeVerifyFailed)
	}
	extracted := filepath.Join(t.TempDir(), "cli")
	if _, err := ExtractBinaryBundle(t.Context(), destination, "tools", extracted); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(extracted, "cli"))
	if err != nil || string(payload) != "exact executable" {
		t.Fatalf("CLI changed: %q %v", payload, err)
	}
}

func TestRegistryCompatibilityNestedDiagnosticPublication(t *testing.T) {
	server := artifactTypeRejectingRegistry(t)
	request := releasePublicationRequestWithRepresentation(t, "", "", OCINVKRCompatibleV1)
	request.Release = nil
	request.Objects = request.Objects[:1]
	request.Objects[0].Output = nil
	root := filepath.Join(t.TempDir(), "diagnostic")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	repository := strings.TrimPrefix(server.URL, "http://") + "/diagnostic"
	ref, err := Push(t.Context(), root, repository, options)
	if err != nil || ref != repository+"@"+result.RootDigest {
		t.Fatalf("nested diagnostic publication: %s %v", ref, err)
	}
	if _, err := Pull(t.Context(), ref, filepath.Join(t.TempDir(), "pulled"), options); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryCompatibilityPushFailureDoesNotCommitRoot(t *testing.T) {
	root, result := registryCompatibilityPublication(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/manifests/"+result.RootDigest) {
			http.Error(w, "fixture refuses final root", http.StatusBadRequest)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	repository := strings.TrimPrefix(server.URL, "http://") + "/product/releases"
	reference, err := Push(t.Context(), root, repository, options)
	assertRegistryCode(t, err, e.CodeRegistryRejected)
	if reference != "" {
		t.Fatalf("failed publication returned success reference %s", reference)
	}
	pin, err := name.NewDigest(repository+"@"+result.RootDigest, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Get(pin, registryOptions(t.Context(), options)...); err == nil {
		t.Fatal("failed publication exposed completed root")
	}
	// Payload uploads preceding the failed commit remain available, not erased
	// or reported as a completed publication.
	inspection, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Name != "chart" {
			continue
		}
		chart, err := name.NewDigest(strings.TrimSuffix(repository, "/releases")+"/chart@"+object.DescriptorDigest, name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := remote.Get(chart, registryOptions(t.Context(), options)...); err != nil {
			t.Fatalf("failed root removed prior chart upload: %v", err)
		}
	}
}

func TestRegistryCompatibilityHelmConsumption(t *testing.T) {
	if os.Getenv("NVKR_TEST_HELM_REGISTRY") != "1" {
		t.Skip("set NVKR_TEST_HELM_REGISTRY=1 to run Helm registry interoperability")
	}
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	server := artifactTypeRejectingRegistry(t)
	root, _ := registryCompatibilityPublication(t)
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	repository := strings.TrimPrefix(server.URL, "http://") + "/product"
	if _, err := Push(t.Context(), root, repository+"/releases", options); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	var chart PublishedObject
	for _, object := range inspection.Root.Objects {
		if object.Name == "chart" {
			chart = object
		}
	}
	manifest, err := flatManifest(t.Context(), root, chart)
	if err != nil {
		t.Fatal(err)
	}
	want, err := readBlob(t.Context(), root, manifest.Layers[0], 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, helm, args...)
		command.Env = append(os.Environ(), "HELM_REGISTRY_CONFIG="+filepath.Join(scratch, "registry.json"),
			"HELM_CACHE_HOME="+scratch, "HELM_CONFIG_HOME="+scratch, "HELM_DATA_HOME="+scratch)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("helm %v: %v: %s", args, err, raw)
		}
		return raw
	}
	t.Logf("Helm client: %s", strings.TrimSpace(string(run("version", "--short"))))
	shown := run("show", "chart", "oci://"+repository+"/chart", "--version", "1.2.3", "--plain-http")
	if !bytes.Contains(shown, []byte("version: 1.2.3")) {
		t.Fatalf("unexpected chart metadata: %s", shown)
	}
	run("pull", "oci://"+repository+"/chart", "--version", "1.2.3", "--plain-http", "--destination", scratch)
	archives, err := filepath.Glob(filepath.Join(scratch, "*.tgz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("Helm did not produce exactly one chart archive: %v %v", archives, err)
	}
	got, err := os.ReadFile(archives[0])
	if err != nil || !bytes.Equal(got, want) || digestBytes(got) != chart.BlobDigest {
		t.Fatalf("Helm changed chart archive: %v", err)
	}
}

// Exercise the public readers with retained and conflicting wrappers, rather
// than testing only the annotation lookup helper.
func TestRegistryCompatibilityTypeRepresentations(t *testing.T) {
	for _, kind := range []string{"helm", "binary", "bundle", "debian", "metadata"} {
		for _, representation := range []string{"legacy", "annotated", "matching", "conflicting", "missing", "untyped empty config", "payload config"} {
			t.Run(kind+"/"+representation, func(t *testing.T) {
				var root string
				var check func() error
				switch kind {
				case "helm":
					root, _ = helmMetadataLayoutWithRepresentation(t, OCINVKRCompatibleV1)
					check = func() error { _, _, err := ReadArtifact(t.Context(), root); return err }
				case "binary":
					root = releasePublicationRequestWithRepresentation(t, "", "", OCINVKRCompatibleV1).Objects[0].Path
					check = func() error { _, _, err := InspectBinaryBundle(t.Context(), root); return err }
				case "bundle":
					archive := helmChartArchiveFixture(t, map[string]string{"files/item.txt": "synthetic bundle file"})
					source := filepath.Join(t.TempDir(), "bundle.tgz")
					if err := os.WriteFile(source, archive, 0o600); err != nil {
						t.Fatal(err)
					}
					root = filepath.Join(t.TempDir(), "bundle")
					if _, err := BuildBundleArtifactWithRepresentation(t.Context(), root, "files", source, digestBytes(archive), OCINVKRCompatibleV1); err != nil {
						t.Fatal(err)
					}
					check = func() error { _, _, err := ReadArtifact(t.Context(), root); return err }
				case "debian":
					payload := []byte("synthetic original package bytes")
					source := filepath.Join(t.TempDir(), "package.deb")
					if err := os.WriteFile(source, payload, 0o600); err != nil {
						t.Fatal(err)
					}
					root = filepath.Join(t.TempDir(), "carrier")
					if _, err := BuildDebianArtifactWithRepresentation(t.Context(), root, "package", source, digestBytes(payload), OCINVKRCompatibleV1); err != nil {
						t.Fatal(err)
					}
					check = func() error { _, err := InspectDebianArtifact(t.Context(), root, digestBytes(payload)); return err }
				case "metadata":
					full := filepath.Join(t.TempDir(), "full")
					if _, err := BuildPublicationRoot(t.Context(), full, releasePublicationRequestWithRepresentation(t, "", "", OCINVKRCompatibleV1)); err != nil {
						t.Fatal(err)
					}
					root = filepath.Join(t.TempDir(), "metadata")
					if _, err := ExportMetadata(t.Context(), full, root); err != nil {
						t.Fatal(err)
					}
					check = func() error { _, err := InspectMetadata(t.Context(), root); return err }
				}
				index, manifest := fixtureManifest(t, root)
				original := index.Manifests[0]
				typeName := manifest.artifactType()
				switch representation {
				case "legacy":
					manifest.ArtifactType = typeName
					delete(manifest.Annotations, artifactTypeAnnotation)
					if manifest.plainConfig() {
						manifest.Config.MediaType = MediaEmptyConfig
					}
					if index.artifactType() != "" {
						index.ArtifactType = index.artifactType()
						delete(index.Annotations, artifactTypeAnnotation)
					}
					delete(index.Annotations, ociRepresentationAnnotation)
				case "matching":
					manifest.ArtifactType = typeName
					if kind == "metadata" {
						index.ArtifactType = MediaPublicationRoot
						index.Annotations[ociRepresentationAnnotation] = string(OCIArtifactV1)
					}
				case "conflicting":
					manifest.ArtifactType = "application/vnd.example.conflict.v1"
				case "missing":
					manifest.ArtifactType = ""
					delete(manifest.Annotations, artifactTypeAnnotation)
				case "untyped empty config":
					manifest.Config.MediaType = MediaEmptyConfig
				case "payload config":
					manifest.Config.MediaType = "application/vnd.cyclonedx+json"
				}
				writer := layoutWriter{root: root}
				descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
				if err != nil {
					t.Fatal(err)
				}
				descriptor.ArtifactType, descriptor.Annotations = original.ArtifactType, original.Annotations
				index.Manifests[0] = descriptor
				if _, err := writer.writeIndex(index); err != nil {
					t.Fatal(err)
				}
				if descriptor.Digest != original.Digest {
					if err := os.Remove(fixtureBlobPath(root, original.Digest)); err != nil {
						t.Fatal(err)
					}
				}
				err = check()
				if representation == "conflicting" || representation == "missing" || representation == "untyped empty config" || representation == "payload config" {
					assertRegistryCode(t, err, e.CodeVerifyFailed)
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
