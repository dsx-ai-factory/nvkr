// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Model GitLab's database media-type admission: unknown artifactType and
// config.mediaType values fail (gitlab-org/container-registry v4.0.0-gitlab,
// registry/datastore/manifest.go:Create and the media_types seed migration).
// This is a registry compatibility contract, not a live GitLab test.
func helmCompatibilityRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	backend := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") && r.Header.Get("Content-Type") == MediaOCIManifest {
			raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var manifest ociManifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			rejectedType := manifest.ArtifactType
			if rejectedType == "" && manifest.Config.MediaType != MediaHelmConfig && manifest.Config.MediaType != MediaOctetStream {
				rejectedType = manifest.Config.MediaType
			}
			if rejectedType != "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"errors":[{"code":"MANIFEST_INVALID","message":"unknown media type: %s"}]}`, rejectedType)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestHelmStandardPublication(t *testing.T) {
	for _, attested := range []bool{false, true} {
		t.Run(fmt.Sprintf("attested=%t", attested), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			root, pin := helmPublicationWithRepresentation(t, "amd64", "", nil, OCIHelmStandardV1)
			_, original, err := ReadObject(ctx, root, "chart")
			if err != nil {
				t.Fatal(err)
			}
			if attested {
				out := filepath.Join(t.TempDir(), "attested")
				result, err := AttestPreparedEvidence(ctx, root, out, func(_ context.Context, subject string, document []byte) ([]byte, error) {
					// Encoding fixture only: this does not exercise a signing service.
					return imageIndexFixtureAttestation(subject, document)
				})
				if err != nil {
					t.Fatal(err)
				}
				root, pin = out, result.RootDigest
			}
			server := helmCompatibilityRegistry(t)
			if attested {
				// Standard Sigstore envelopes retain their registered artifact type;
				// legacy GitLab acceptance of that separate format is not claimed.
				server = httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
				t.Cleanup(server.Close)
			}
			prefix := strings.TrimPrefix(server.URL, "http://") + "/candidate"
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			ref, err := Push(ctx, root, prefix+"/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("complete helm-standard-v1 publication: %s", ref)
			pulled := filepath.Join(t.TempDir(), "pulled")
			inspection, err := Pull(ctx, ref, pulled, options)
			if err != nil || inspection.RootDigest != pin {
				t.Fatalf("complete closure readback: %+v, %v", inspection, err)
			}
			if inspection.Root.OCIRepresentation() != OCIHelmStandardV1 {
				t.Fatal("lost explicit representation")
			}
			if attested {
				if err := RequireAttestedEvidence(inspection); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := PullMetadata(ctx, ref, filepath.Join(t.TempDir(), "metadata"), options); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "selected")
			if _, err := PullOutput(ctx, ref, "chart", out, t.TempDir(), options); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(out, "chart.tgz"))
			if err != nil || !bytes.Equal(raw, original) {
				t.Fatalf("chart archive changed: %v", err)
			}
			for _, object := range inspection.Root.Objects {
				manifest, err := metadataManifest(ctx, pulled, object)
				if err != nil {
					t.Fatal(err)
				}
				if object.Evidence == nil {
					if manifest.ArtifactType != "" || manifest.Config.MediaType != MediaHelmConfig || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != MediaHelmChartLayer || manifest.Layers[0].Digest != digestBytes(original) {
						t.Fatal("nonstandard Helm manifest or changed archive")
					}
					continue
				}
				if attested {
					if manifest.ArtifactType != sbom.BundleMediaType || manifest.Config.MediaType != MediaEmptyConfig || manifest.Subject == nil || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != sbom.BundleMediaType {
						t.Fatal("signed evidence lost standard envelope")
					}
				} else if manifest.ArtifactType != "" || manifest.Config.MediaType != MediaOctetStream {
					t.Fatal("raw evidence has an unsupported wrapper")
				}
				if _, _, err := EvidenceDocument(ctx, pulled, object.Name); err != nil {
					t.Fatal(err)
				}
			}
			t.Run("stock Helm", func(t *testing.T) {
				if os.Getenv("NVKR_TEST_HELM_OCI") != "1" {
					t.Skip("set NVKR_TEST_HELM_OCI=1 to exercise the installed Helm binary")
				}
				directory := t.TempDir()
				run := func(args ...string) []byte {
					t.Helper()
					command := exec.CommandContext(ctx, "helm", args...)
					command.Dir = directory
					command.Env = append(os.Environ(), "HELM_CACHE_HOME="+filepath.Join(directory, "cache"),
						"HELM_CONFIG_HOME="+filepath.Join(directory, "config"), "HELM_DATA_HOME="+filepath.Join(directory, "data"))
					command.WaitDelay = 5 * time.Second
					output, err := command.CombinedOutput()
					t.Logf("$ helm %s\n%s", strings.Join(args, " "), output)
					if err != nil {
						t.Fatal(err)
					}
					return output
				}
				run("version", "--short")
				chartRef := "oci://" + prefix + "/chart"
				run("pull", chartRef, "--version", "1.2.3", "--plain-http", "--destination", directory)
				chart, err := os.ReadFile(filepath.Join(directory, "chart-1.2.3.tgz"))
				if err != nil || !bytes.Equal(chart, original) {
					t.Fatalf("stock Helm changed chart bytes: %v", err)
				}
				t.Logf("original and helm pull archive SHA-256: %s", digestBytes(chart))
				run("show", "chart", chartRef, "--version", "1.2.3", "--plain-http")
				rendered := run("template", "demo", chartRef, "--version", "1.2.3", "--plain-http")
				if !bytes.Contains(rendered, []byte("registry.example.test/controller@sha256:"+strings.Repeat("a", 64))) {
					t.Fatal("rendered chart lost its exact image digest")
				}
			})
		})
	}
}

func TestHelmLegacyTypeCompatibility(t *testing.T) {
	for _, artifactType := range []string{"", "application/vnd.nvkr.helm-bundle.v1", "application/vnd.example.wrong.v1"} {
		t.Run("type="+artifactType, func(t *testing.T) {
			root, archive := helmMetadataLayoutWithRepresentation(t, OCIHelmStandardV1)
			_, manifest := fixtureManifest(t, root)
			manifest.ArtifactType = artifactType
			descriptor, err := (layoutWriter{root: root}).addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (layoutWriter{root: root}).writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}}); err != nil {
				t.Fatal(err)
			}
			object := PublishedObject{Name: "chart", ArtifactType: "application/vnd.nvkr.helm-bundle.v1", DescriptorDigest: descriptor.Digest, Size: descriptor.Size,
				BlobDigest: digestBytes(archive), Output: &OutputBinding{Format: outputFormatHelm}}
			_, metadataErr := publishedHelmChartConfig(t.Context(), root, object)
			bindingErr := verifyPublishedObjectBinding(t.Context(), root, object, descriptor, nil)
			if strings.Contains(artifactType, "wrong") {
				assertRegistryCode(t, metadataErr, e.CodeVerifyFailed)
				assertRegistryCode(t, bindingErr, e.CodeVerifyFailed)
				_, err := pullHelmOutput(t.Context(), "unused", filepath.Join(t.TempDir(), "out"), root, t.TempDir(), object, RegistryOptions{})
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				return
			}
			if metadataErr != nil || bindingErr != nil {
				t.Fatalf("compatible chart rejected: %v / %v", metadataErr, bindingErr)
			}
			server := helmCompatibilityRegistry(t)
			ref, err := name.NewDigest(strings.TrimPrefix(server.URL, "http://")+"/chart@"+descriptor.Digest, name.Insecure)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := v1.NewHash(descriptor.Digest)
			if err != nil {
				t.Fatal(err)
			}
			img, err := layout.Path(root).Image(hash)
			if err != nil {
				t.Fatal(err)
			}
			err = remote.Write(ref, img, remote.WithContext(t.Context()))
			if artifactType != "" {
				if err == nil || !strings.Contains(err.Error(), "MANIFEST_INVALID") {
					t.Fatalf("legacy chart did not reproduce registry rejection: %v", err)
				}
				t.Logf("reproduced legacy rejection: %v", err)
			} else if err != nil {
				t.Fatal(err)
			}
			// Old references remain consumable from a registry that stored them.
			legacyServer := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
			defer legacyServer.Close()
			legacyRef, err := name.NewDigest(strings.TrimPrefix(legacyServer.URL, "http://")+"/chart@"+descriptor.Digest, name.Insecure)
			if err != nil {
				t.Fatal(err)
			}
			if err := remote.Write(legacyRef, img, remote.WithContext(t.Context())); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "out")
			if _, err := pullHelmOutput(t.Context(), legacyRef.Name(), out, root, t.TempDir(), object, RegistryOptions{PlainHTTP: true, Anonymous: true}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(out, "chart.tgz"))
			if err != nil || !bytes.Equal(got, archive) {
				t.Fatalf("legacy read changed chart bytes: %v", err)
			}
		})
	}
}

func TestHelmEvidenceOpaqueConfigBindsPublicationRecord(t *testing.T) {
	for _, scenario := range []string{"exact", "name", "artifact type", "semantic digest", "payload digest", "missing evidence membership"} {
		t.Run(scenario, func(t *testing.T) {
			root, _ := helmPublicationWithRepresentation(t, "amd64", "", nil, OCIHelmStandardV1)
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			var evidence PublishedObject
			for _, object := range inspection.Root.Objects {
				if object.Evidence != nil {
					evidence = object
				}
			}
			switch scenario {
			case "name":
				evidence.Name = "substituted"
			case "artifact type":
				evidence.ArtifactType = "application/vnd.example.wrong.v1"
			case "semantic digest":
				evidence.SemanticDigest = digestBytes([]byte("substituted"))
			case "payload digest":
				evidence.BlobDigest = digestBytes([]byte("substituted"))
			case "missing evidence membership":
				evidence.Evidence = nil
			}
			_, err = metadataManifest(t.Context(), root, evidence)
			if scenario == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestHelmStandardRootProfileBinding(t *testing.T) {
	for _, selection := range []OCIRepresentation{"", OCIArtifactV1, OCINVKRCompatibleV1, OCIHelmStandardV1} {
		t.Run(string(selection), func(t *testing.T) {
			root, _ := helmPublicationWithRepresentation(t, "amd64", "", nil, OCIHelmStandardV1)
			index, raw, err := readIndex(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := readPublicationManifest(t.Context(), root, index.Manifests[0])
			if err != nil {
				t.Fatal(err)
			}
			err = validateRootRepresentation(selection, manifest)
			if selection != OCIHelmStandardV1 {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "metadata")
			result, err := ExportMetadataWithExpectedRoot(t.Context(), root, out, digestBytes(raw))
			if err != nil || result.RootDigest != digestBytes(raw) {
				t.Fatalf("metadata export changed identity: %v", err)
			}
		})
	}
}

func TestHelmStandardRejectsSubstituteSignedEnvelope(t *testing.T) {
	out := filepath.Join(t.TempDir(), "rejected")
	_, err := BuildPublicationRoot(t.Context(), out, PublicationRequest{OCIRepresentation: OCIHelmStandardV1,
		Objects: []ObjectInput{{Name: "evidence", Kind: "sbom", ArtifactType: sbom.BundleMediaType, Bytes: []byte("{}"), SemanticDigest: digestBytes([]byte("{}"))}}})
	assertRegistryCode(t, err, e.CodeEngineCapability)
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("unsupported signed-profile construction exposed output")
	}
}
