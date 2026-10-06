package delivery

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// This is a transport-only diagnostic fixture. Its descriptive build fields do
// not stand for native CWE production, signed SBOMs or authenticated release use.
func registryImageIndexFixture(t *testing.T) (string, Result, ImageIndexDescription) {
	t.Helper()
	index := assembledImageIndex(t, imageIndexInputs(t))
	description, err := index.Description()
	if err != nil {
		t.Fatal(err)
	}
	path := writeIndexFixture(t, index)
	binding := &ImageIndexBinding{RequiredPlatforms: append([]string(nil), imageIndexPlatforms...)}
	var builds []ir.ImageIndexBuildChild
	for _, child := range description.Children {
		build := digestBytes([]byte("build " + child.Platform))
		binding.Children = append(binding.Children, ImageIndexChildBinding{Platform: child.Platform,
			ManifestDigest: child.ManifestDigest, Size: child.Size, BuildDigest: build,
			Component: "controller", BuildSubject: digestBytes([]byte("export " + child.Platform))})
		builds = append(builds, ir.ImageIndexBuildChild{Platform: child.Platform, BuildDigest: build, Component: "controller"})
	}
	build, err := ir.ImageIndexBuildDigest(imageIndexPlatforms, builds)
	if err != nil {
		t.Fatal(err)
	}
	objects := []ObjectInput{{
		Name: "cwe", Kind: "container-image", ArtifactType: MediaOCIIndex, Path: path, Layout: true,
		SemanticDigest: description.Digest, BlobDigest: description.Digest,
		Output: &OutputBinding{Repository: "cwe", Format: "oci", BuildDigest: build, Components: []string{"controller"}, ImageIndex: binding},
	}}
	for _, child := range binding.Children {
		bom, err := sbom.NewSubject(child.BuildDigest, child.Component, child.ManifestDigest)
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.ForSubject(bom, child.ManifestDigest, "build-output", child.BuildSubject)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, ObjectInput{
			Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom",
			ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json",
			SemanticDigest: semantic, Bytes: raw,
			Evidence: &EvidenceBinding{Outputs: []string{"cwe"}, Platform: child.Platform, Component: child.Component,
				Stage: "build-output", SubjectKind: "oci-manifest", SubjectDigest: child.ManifestDigest,
				InspectionInputDigest: child.BuildSubject},
		})
	}
	content, err := ContentDigest([]Output{{Name: "cwe", Kind: "oci", Digest: description.Digest,
		BuildDigest: build, Components: []string{"controller"}, ImageIndex: binding}})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), out, PublicationRequest{
		Release: &ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, ContentSubject: content}, Objects: objects,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, result, description
}

func TestRegistryImageIndexRoundTripPreservesEveryChild(t *testing.T) {
	publication, result, expected := registryImageIndexFixture(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var mu sync.Mutex
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") {
			mu.Lock()
			writes = append(writes, r.URL.Path)
			mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	repository := strings.TrimPrefix(server.URL, "http://") + "/product/release"
	ref, err := Push(t.Context(), publication, repository, options)
	if err != nil || ref != repository+"@"+result.RootDigest {
		t.Fatalf("push=%s/%v", ref, err)
	}
	childRef, err := name.NewDigest(strings.TrimPrefix(server.URL, "http://")+"/product/cwe@"+expected.Digest, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := remote.Index(childRef, registryOptions(t.Context(), options)...)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := standard.IndexManifest()
	if err != nil || len(manifest.Manifests) != len(expected.Children) {
		t.Fatalf("standard index=%+v/%v", manifest, err)
	}
	captured, err := fetchRegistryImageIndex(t.Context(), childRef, expected.Size, imageIndexPlatforms, registryOptions(t.Context(), options))
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close()
	got, err := captured.Description()
	if err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("captured=%+v/%v, want %+v", got, err, expected)
	}
	out := filepath.Join(t.TempDir(), "pulled")
	inspection, err := Pull(t.Context(), ref, out, options)
	if err != nil || inspection.RootDigest != result.RootDigest {
		t.Fatalf("pull=%+v/%v", inspection, err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Name != "cwe" {
			continue
		}
		held, err := openPublishedImageIndex(t.Context(), out, object)
		if err != nil {
			t.Fatal(err)
		}
		got, err := held.Description()
		_ = held.Close()
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatalf("pulled index=%+v/%v", got, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(publication, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		original := mustReadImageFile(t, filepath.Join(publication, "blobs", "sha256", entry.Name()))
		pulled := mustReadImageFile(t, filepath.Join(out, "blobs", "sha256", entry.Name()))
		if !bytes.Equal(original, pulled) {
			t.Fatalf("roundtrip changed %s", entry.Name())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(writes) == 0 || !strings.HasSuffix(writes[len(writes)-1], "/manifests/"+result.RootDigest) {
		t.Fatalf("root was not last: %v", writes)
	}
	// The registry fixture lacks OCI's Referrers API. The pinned client keeps
	// SBOM attachments in its subject-digest fallback index; this is distinct
	// from giving the runnable image an operator-selected discovery alias.
	fallback := "/v2/product/cwe/manifests/" + strings.Replace(expected.Digest, ":", "-", 1)
	for _, path := range writes {
		if !strings.Contains(path, "/manifests/sha256:") && path != fallback {
			t.Fatalf("unexpected image/root discovery tag write %s", path)
		}
	}
	referrers, err := remote.Referrers(childRef, registryOptions(t.Context(), options)...)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := referrers.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	expectedEvidence := map[string]bool{}
	for _, object := range inspection.Root.Objects {
		if object.Evidence != nil {
			expectedEvidence[object.DescriptorDigest] = true
		}
	}
	if len(attachments.Manifests) != len(expectedEvidence) {
		t.Fatalf("referrer fallback has unexpected membership: %+v", attachments)
	}
	for _, descriptor := range attachments.Manifests {
		if !expectedEvidence[descriptor.Digest.String()] || descriptor.ArtifactType != "application/vnd.cyclonedx+json" {
			t.Fatalf("referrer fallback does not exclusively contain exact SBOM attachments: %+v", descriptor)
		}
	}
}

func TestRegistryImageIndexRejectsUnreadableChildBeforeRoot(t *testing.T) {
	for _, mode := range []string{"missing-manifest", "missing-layer", "corrupt-layer"} {
		t.Run(mode, func(t *testing.T) {
			publication, result, description := registryImageIndexFixture(t)
			child := description.Children[0]
			manifest, err := flatManifest(t.Context(), publication, PublishedObject{DescriptorDigest: child.ManifestDigest, Size: child.Size})
			if err != nil {
				t.Fatal(err)
			}
			target := "/v2/product/cwe/manifests/" + child.ManifestDigest
			wantCode := e.CodeUnavailable
			if mode != "missing-manifest" {
				target = "/v2/product/cwe/blobs/" + manifest.Layers[0].Digest
			}
			if mode == "corrupt-layer" {
				wantCode = e.CodeVerifyFailed
			}
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			var rootWrites atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v2/product/release/manifests/") {
					rootWrites.Add(1)
				}
				if r.Method == http.MethodGet && r.URL.Path == target {
					if mode == "corrupt-layer" {
						_, _ = io.WriteString(w, "substituted layer")
					} else {
						http.NotFound(w, r)
					}
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			repository := strings.TrimPrefix(server.URL, "http://") + "/product/release"
			ref, err := Push(t.Context(), publication, repository+":candidate", options)
			assertRegistryCode(t, err, wantCode)
			if ref != "" || rootWrites.Load() != 0 {
				t.Fatalf("failed closure exposed root %s (%d writes)", ref, rootWrites.Load())
			}
			_, err = ResolveDigest(t.Context(), repository+"@"+result.RootDigest, options)
			assertRegistryCode(t, err, e.CodeUnavailable)
		})
	}
}

func TestRegistryImageIndexPullFailureDoesNotExposePartialLayout(t *testing.T) {
	publication, _, description := registryImageIndexFixture(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var broken atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken.Load() && r.Method == http.MethodGet && r.URL.Path == "/v2/product/cwe/manifests/"+description.Children[1].ManifestDigest {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	ref, err := Push(t.Context(), publication, strings.TrimPrefix(server.URL, "http://")+"/product/release", options)
	if err != nil {
		t.Fatal(err)
	}
	broken.Store(true)
	out := filepath.Join(t.TempDir(), "must-not-exist")
	_, err = Pull(t.Context(), ref, out, options)
	assertRegistryCode(t, err, e.CodeUnavailable)
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed pull exposed a partial layout: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed pull leaked staging: %v/%v", entries, err)
	}
}

func TestRegistryImageIndexHeldCaptureUsesExistingImageOwner(t *testing.T) {
	index := assembledImageIndex(t, imageIndexInputs(t))
	want, err := index.Description()
	if err != nil {
		t.Fatal(err)
	}
	standard, cleanup, err := registryIndexLayout(t.Context(), index)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	ref, err := name.NewDigest(strings.TrimPrefix(server.URL, "http://")+"/image@"+want.Digest, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	options := registryOptions(t.Context(), RegistryOptions{PlainHTTP: true, Anonymous: true})
	if err := remote.WriteIndex(ref, standard, options...); err != nil {
		t.Fatal(err)
	}
	got, err := fetchRegistryImageIndex(t.Context(), ref, want.Size, imageIndexPlatforms, options)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	description, err := got.Description()
	if err != nil || !reflect.DeepEqual(description, want) {
		t.Fatalf("captured=%+v/%v, want %+v", description, err, want)
	}
	_, err = fetchRegistryImageIndex(t.Context(), ref, want.Size, []string{"linux/amd64"}, options)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	_, err = fetchRegistryImageIndex(t.Context(), ref, want.Size+1, imageIndexPlatforms, options)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
}
