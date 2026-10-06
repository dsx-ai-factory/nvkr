package delivery

import (
	"context"
	"encoding/json"
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
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestImageTagSyntaxAdmission(t *testing.T) {
	for _, test := range []struct {
		name  string
		tags  map[string]string
		valid bool
	}{
		{"absent", nil, true}, {"empty", map[string]string{}, true},
		{"version", map[string]string{"cwe": "v1.2.3-rc.1_build"}, true},
		{"underscore", map[string]string{"cwe": "_channel"}, true},
		{"limit", map[string]string{"cwe": strings.Repeat("a", 128)}, true},
		{"empty tag", map[string]string{"cwe": ""}, false},
		{"empty output", map[string]string{"": "v1"}, false},
		{"selector", map[string]string{"cwe/*": "v1"}, false},
		{"leading dot", map[string]string{"cwe": ".v1"}, false},
		{"leading dash", map[string]string{"cwe": "-v1"}, false},
		{"long", map[string]string{"cwe": strings.Repeat("a", 129)}, false},
		{"repository", map[string]string{"cwe": "repo:v1"}, false},
		{"path", map[string]string{"cwe": "other/v1"}, false},
		{"digest", map[string]string{"cwe": "sha256:" + strings.Repeat("a", 64)}, false},
		{"referrer", map[string]string{"cwe": "sha256-" + strings.Repeat("a", 64)}, false},
		{"signature", map[string]string{"cwe": "sha256-" + strings.Repeat("a", 64) + ".sig"}, false},
		{"reserved prefix", map[string]string{"cwe": "sha256-channel"}, false},
		{"template", map[string]string{"cwe": "${version}"}, false},
		{"space", map[string]string{"cwe": "v1 "}, false},
		{"control", map[string]string{"cwe": "v1\n"}, false},
		{"unicode", map[string]string{"cwe": "rélease"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateImageTags(test.tags)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeInvalidCatalog)
				_, err = PushWithOptions(t.Context(), filepath.Join(t.TempDir(), "absent"), "invalid", PushOptions{ImageTags: test.tags})
				assertRegistryCode(t, err, e.CodeInvalidCatalog)
			}
		})
	}
	tags := make(map[string]string, maxDeliveryObjects+1)
	for i := range maxDeliveryObjects + 1 {
		tags[strings.Repeat("a", i+1)] = "v1"
	}
	assertRegistryCode(t, ValidateImageTags(tags), e.CodeInvalidCatalog)
	assertRegistryCode(t, (PushOptions{Registry: RegistryOptions{Anonymous: true, Username: "conflict"}}).Validate(), e.CodeUsage)
}

func TestImageTagSelectionRejectsBeforeNetwork(t *testing.T) {
	publication, _, _ := registryImageIndexFixture(t)
	inspection, err := Inspect(t.Context(), publication)
	if err != nil {
		t.Fatal(err)
	}
	native, _ := releasePublicationFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	for _, test := range []struct{ name, path, output string }{
		{"missing", publication, "missing"},
		{"evidence", publication, inspection.Root.Objects[1].Name},
		{"non OCI", native, "tools"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref, err := PushWithOptions(t.Context(), test.path, strings.TrimPrefix(server.URL, "http://")+"/product/release",
				PushOptions{Registry: RegistryOptions{PlainHTTP: true, Anonymous: true}, ImageTags: map[string]string{test.output: "v1"}})
			assertRegistryCode(t, err, e.CodeInvalidCatalog)
			if ref != "" || requests.Load() != 0 {
				t.Fatal("invalid selection contacted registry")
			}
		})
	}
	rootRef, err := name.NewDigest("registry.test/product/release@"+inspection.RootDigest, name.StrictValidation)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := publicationReferences(rootRef, inspection.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	// These descriptions cannot be produced by the constructor, but the preflight
	// itself must still reject ambiguous names and native single-image selections.
	for _, duplicate := range []bool{false, true} {
		root := inspection.Root
		root.Objects = append([]PublishedObject(nil), root.Objects...)
		if duplicate {
			root.Objects = append(root.Objects, root.Objects[0])
		} else {
			binding := *root.Objects[0].Output
			binding.ImageIndex = nil
			root.Objects[0].Output = &binding
		}
		_, err := prepareImageAliases(root, refs, map[string]string{"cwe": "v1"})
		assertRegistryCode(t, err, e.CodeInvalidCatalog)
	}
}

// Two aggregates have the same native children but distinct valid index byte
// ordering. Shared SBOMs retain both exact named output memberships.
func twoAliasPublication(t *testing.T) (string, Result, map[string]string) {
	t.Helper()
	request := imageIndexPublicationRequestFixture(t)
	first := request.Objects[0]
	first.Name, first.Output.Repository = "zeta", "zeta"
	second := first
	binding := *first.Output
	binding.Repository = "alpha"
	binding.ImageIndex = first.Output.ImageIndex.Clone()
	second.Output, second.Name = &binding, "alpha"
	held, err := OpenImageIndex(t.Context(), first.Path, first.BlobDigest, first.Output.ImageIndex.RequiredPlatforms)
	if err != nil {
		t.Fatal(err)
	}
	second.Path = writeIndexFixture(t, held)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(second.Path, "index.json")
	var index runnableIndex
	if err := json.Unmarshal(mustReadImageFile(t, path), &index); err != nil {
		t.Fatal(err)
	}
	index.Manifests[0], index.Manifests[1] = index.Manifests[1], index.Manifests[0]
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	second.BlobDigest, second.SemanticDigest = digestBytes(raw), digestBytes(raw)
	request.Objects[0] = first
	request.Objects = append(request.Objects, second)
	for i := range request.Objects {
		if request.Objects[i].Evidence != nil {
			request.Objects[i].Evidence.Outputs = []string{"alpha", "zeta"}
		}
	}
	outputs := []Output{}
	for _, input := range []ObjectInput{first, second} {
		outputs = append(outputs, Output{Name: input.Name, Kind: "oci", Digest: input.BlobDigest,
			BuildDigest: input.Output.BuildDigest, Components: input.Output.Components, ImageIndex: input.Output.ImageIndex})
	}
	request.Release.ContentSubject, err = ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), out, request)
	if err != nil {
		t.Fatal(err)
	}
	return out, result, map[string]string{"alpha": second.BlobDigest, "zeta": first.BlobDigest}
}

func TestImageTagsFollowCompleteReadbackInSortedOrderAndRerunWithoutPUT(t *testing.T) {
	publication, result, expected := twoAliasPublication(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var mu sync.Mutex
	var events []string
	var immutable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		events = append(events, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if immutable.Load() && r.Method == http.MethodPut &&
			(r.URL.Path == "/v2/product/alpha/manifests/v1" || r.URL.Path == "/v2/product/zeta/manifests/candidate") {
			w.WriteHeader(http.StatusConflict)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/product/release"
	options := PushOptions{Registry: RegistryOptions{PlainHTTP: true, Anonymous: true}, ImageTags: map[string]string{"zeta": "candidate", "alpha": "v1"}}
	for range 2 {
		ref, err := PushWithOptions(t.Context(), publication, repository, options)
		if err != nil || ref != repository+"@"+result.RootDigest {
			t.Fatalf("push=%s/%v", ref, err)
		}
		immutable.Store(true)
	}
	for output, digest := range expected {
		tag, err := name.NewTag(strings.TrimPrefix(server.URL, "http://")+"/product/"+output+":"+options.ImageTags[output], name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		got, err := remote.Get(tag, registryOptions(t.Context(), options.Registry)...)
		if err != nil || got.Digest.String() != digest || string(got.MediaType) != MediaOCIIndex {
			t.Fatalf("alias=%+v/%v", got, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	var aliasWrites []string
	rootRead := false
	for _, event := range events {
		if event == "GET /v2/product/release/manifests/"+result.RootDigest {
			rootRead = true
		}
		if event == "PUT /v2/product/alpha/manifests/v1" || event == "PUT /v2/product/zeta/manifests/candidate" {
			if !rootRead {
				t.Fatal("alias advanced before exact root readback")
			}
			aliasWrites = append(aliasWrites, event)
		}
	}
	if !reflect.DeepEqual(aliasWrites, []string{"PUT /v2/product/alpha/manifests/v1", "PUT /v2/product/zeta/manifests/candidate"}) {
		t.Fatalf("alias order or idempotence differs: %v", aliasWrites)
	}
}

func TestImageTagsDoNotAdvanceAfterAnyClosureReadbackFailure(t *testing.T) {
	for _, mode := range []string{"child missing", "layer corrupt", "evidence missing", "root missing", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			publication, result, index := registryImageIndexFixture(t)
			inspection, err := Inspect(t.Context(), publication)
			if err != nil {
				t.Fatal(err)
			}
			child, err := flatManifest(t.Context(), publication, PublishedObject{DescriptorDigest: index.Children[0].ManifestDigest, Size: index.Children[0].Size})
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := flatManifest(t.Context(), publication, inspection.Root.Objects[1])
			if err != nil {
				t.Fatal(err)
			}
			target := "/v2/product/cwe/manifests/" + index.Children[0].ManifestDigest
			want := e.CodeUnavailable
			switch mode {
			case "layer corrupt":
				target = "/v2/product/cwe/blobs/" + child.Layers[0].Digest
				want = e.CodeVerifyFailed
			case "evidence missing":
				target = "/v2/product/cwe/blobs/" + evidence.Layers[0].Digest
			case "root missing", "canceled":
				target = "/v2/product/release/manifests/" + result.RootDigest
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var rootWritten atomic.Bool
			var aliases atomic.Int32
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut && r.URL.Path == "/v2/product/release/manifests/"+result.RootDigest {
					rootWritten.Store(true)
				}
				if r.Method == http.MethodPut && r.URL.Path == "/v2/product/cwe/manifests/candidate" {
					aliases.Add(1)
				}
				if rootWritten.Load() && r.Method == http.MethodGet && r.URL.Path == target {
					if mode == "canceled" {
						cancel()
					}
					if mode == "layer corrupt" {
						_, _ = io.WriteString(w, "corrupt layer")
					} else {
						http.NotFound(w, r)
					}
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			if mode == "canceled" {
				want = e.CodeTimeout
			}
			workspace := t.TempDir()
			t.Setenv("TMPDIR", workspace)
			ref, err := PushWithOptions(ctx, publication, strings.TrimPrefix(server.URL, "http://")+"/product/release",
				PushOptions{Registry: RegistryOptions{PlainHTTP: true, Anonymous: true}, ImageTags: map[string]string{"cwe": "candidate"}})
			assertRegistryCode(t, err, want)
			entries, readErr := os.ReadDir(workspace)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("readback leaked private workspace: %v/%v", entries, readErr)
			}
			if ref != "" || aliases.Load() != 0 || !rootWritten.Load() {
				t.Fatalf("failed readback advanced alias: %s writes=%d root=%v", ref, aliases.Load(), rootWritten.Load())
			}
		})
	}
}

func TestImageTagMutationFailuresPreserveRetryContract(t *testing.T) {
	for _, mode := range []string{"head transient", "immutable", "put uncertain", "put canceled", "readback missing", "readback corrupt"} {
		t.Run(mode, func(t *testing.T) {
			publication, _, index := registryImageIndexFixture(t)
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var active atomic.Bool
			var writes atomic.Int32
			aliasPath := "/v2/product/cwe/manifests/candidate"
			oldRaw := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == aliasPath && r.Method == http.MethodPut {
					writes.Add(1)
				}
				if active.Load() && r.URL.Path == aliasPath {
					if r.Method == http.MethodHead && mode == "head transient" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if r.Method == http.MethodPut {
						if mode == "immutable" {
							w.WriteHeader(http.StatusConflict)
							return
						}
						if mode == "put uncertain" || mode == "put canceled" {
							stored := httptest.NewRecorder()
							handler.ServeHTTP(stored, r)
							if mode == "put canceled" {
								cancel()
							}
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
					}
					if r.Method == http.MethodGet && writes.Load() > 0 {
						if mode == "readback missing" {
							http.NotFound(w, r)
							return
						}
						if mode == "readback corrupt" {
							w.Header().Set("Content-Type", MediaOCIIndex)
							_, _ = w.Write(oldRaw)
							return
						}
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := PushOptions{Registry: RegistryOptions{PlainHTTP: true, Anonymous: true}, ImageTags: map[string]string{"cwe": "candidate"}}
			tag, err := name.NewTag(strings.TrimPrefix(server.URL, "http://")+"/product/cwe:candidate", name.Insecure)
			if err != nil {
				t.Fatal(err)
			}
			if err := remote.Tag(tag, aliasManifest(oldRaw), registryOptions(t.Context(), options.Registry)...); err != nil {
				t.Fatal(err)
			}
			writes.Store(0)
			active.Store(true)
			repository := strings.TrimPrefix(server.URL, "http://") + "/product/release"
			ref, err := PushWithOptions(ctx, publication, repository, options)
			want := e.CodeUnavailable
			if mode == "immutable" {
				want = e.CodeInvalidCatalog
			}
			if mode == "put canceled" {
				want = e.CodeTimeout
			}
			if mode == "readback corrupt" {
				want = e.CodeVerifyFailed
			}
			assertRegistryCode(t, err, want)
			if ref != "" {
				t.Fatal("failed alias write returned successful reference")
			}
			if mode == "head transient" && writes.Load() != 0 {
				t.Fatal("inconclusive HEAD caused tag PUT")
			}
			if mode != "head transient" && writes.Load() == 0 {
				t.Fatal("failure did not exercise tag write")
			}
			if strings.HasPrefix(mode, "readback") || strings.HasPrefix(mode, "put ") {
				if !strings.Contains(err.Error(), "may have advanced") {
					t.Fatalf("lost uncertain outcome: %v", err)
				}
			}
			active.Store(false)
			got, err := remote.Get(tag, registryOptions(t.Context(), options.Registry)...)
			if err != nil {
				t.Fatal(err)
			}
			expected := index.Digest
			if mode == "head transient" || mode == "immutable" {
				expected = digestBytes(oldRaw)
			}
			if got.Digest.String() != expected {
				t.Fatal("failure unexpectedly changed or rolled back discovery")
			}
			before := writes.Load()
			// Once the registry is readable again, the exact accepted tag is a no-op.
			if mode != "immutable" && mode != "head transient" {
				if _, err := PushWithOptions(t.Context(), publication, repository, options); err != nil {
					t.Fatal(err)
				}
				if writes.Load() != before {
					t.Fatal("retry re-pushed an already accepted alias")
				}
			}
		})
	}
}

func TestImageTagRequiresUnselectedSiblingReadback(t *testing.T) {
	publication, result, indexes := twoAliasPublication(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var rootWritten atomic.Bool
	var aliasWrites atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v2/product/release/manifests/"+result.RootDigest {
			rootWritten.Store(true)
		}
		if r.Method == http.MethodPut && r.URL.Path == "/v2/product/alpha/manifests/candidate" {
			aliasWrites.Add(1)
		}
		if rootWritten.Load() && r.Method == http.MethodGet && r.URL.Path == "/v2/product/zeta/manifests/"+indexes["zeta"] {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ref, err := PushWithOptions(t.Context(), publication, strings.TrimPrefix(server.URL, "http://")+"/product/release",
		PushOptions{Registry: RegistryOptions{PlainHTTP: true, Anonymous: true}, ImageTags: map[string]string{"alpha": "candidate"}})
	assertRegistryCode(t, err, e.CodeUnavailable)
	if ref != "" || !rootWritten.Load() || aliasWrites.Load() != 0 {
		t.Fatal("selected alias did not wait for the unselected sibling's exact readback")
	}
}
