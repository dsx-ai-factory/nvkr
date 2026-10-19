// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

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
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/registry"
	"golang.org/x/sys/unix"
)

func TestPullOutputSelectsDiskAndExecutable(t *testing.T) {
	fixture := pullExecutableFixture(t)
	diskRaw, err := os.ReadFile(filepath.Join(fixture.root, "blobs", "sha256", strings.TrimPrefix(fixture.diskLayer, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, output, kind, path, layer string
		failure                         string
		want                            []byte
		code                            e.Code
	}{
		{name: "disk", output: "node-image", kind: "disk-image", path: "node-image.qcow2", layer: fixture.diskLayer, want: diskRaw},
		{name: "executable", output: "tools", kind: "executable", path: "cli", layer: fixture.layer, want: []byte("exact executable")},
		{name: "corrupt disk", output: "node-image", layer: fixture.diskLayer, failure: "corrupt", code: e.CodeVerifyFailed},
		{name: "existing disk directory", output: "node-image", layer: fixture.diskLayer, failure: "existing", code: e.CodeImmutableViolation},
		{name: "disk destination symlink", output: "node-image", layer: fixture.diskLayer, failure: "symlink", code: e.CodeImmutableViolation},
	} {
		t.Run(test.name, func(t *testing.T) {
			var active atomic.Bool
			var selectedGets, siblingGets atomic.Int64
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if active.Load() && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blobs/") {
					blob := filepath.Base(r.URL.Path)
					if blob == fixture.layer || blob == fixture.diskLayer {
						if blob != test.layer {
							siblingGets.Add(1)
							http.Error(w, "sibling payload forbidden", http.StatusForbidden)
							return
						}
						selectedGets.Add(1)
						if test.failure == "corrupt" {
							raw := bytes.Clone(diskRaw)
							raw[0] ^= 1
							_, _ = w.Write(raw)
							return
						}
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			ref, err := Push(t.Context(), fixture.root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			active.Store(true)
			parent, workspace := t.TempDir(), t.TempDir()
			out := filepath.Join(parent, "output")
			var existing []byte
			if test.failure == "existing" {
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
				existing = []byte("keep")
				if err := os.WriteFile(filepath.Join(out, "sentinel"), existing, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.failure == "symlink" {
				if err := os.Symlink(t.TempDir(), out); err != nil {
					t.Fatal(err)
				}
			}
			result, err := PullOutput(t.Context(), ref, test.output, out, workspace, options)
			if selectedGets.Load() == 0 || siblingGets.Load() != 0 {
				t.Fatalf("wrong payload requests selected=%d siblings=%d", selectedGets.Load(), siblingGets.Load())
			}
			assertEmptyPullDirectory(t, workspace)
			if test.code != "" {
				assertRegistryCode(t, err, test.code)
				if result.Output.Name != "" || len(result.Files) != 0 {
					t.Fatal("failed pull returned a result")
				}
				switch test.failure {
				case "existing":
					raw, err := os.ReadFile(filepath.Join(out, "sentinel"))
					if err != nil || !bytes.Equal(raw, existing) {
						t.Fatalf("destination was replaced: %v", err)
					}
				case "symlink":
					info, err := os.Lstat(out)
					if err != nil || info.Mode()&os.ModeSymlink == 0 {
						t.Fatalf("destination symlink was replaced: %v", err)
					}
				default:
					assertEmptyPullDirectory(t, parent)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Output.Name != test.output || result.Output.Kind != test.kind || len(result.Files) != 1 || result.Files[0].Path != test.path {
				t.Fatalf("wrong pull result: %+v", result)
			}
			raw, err := os.ReadFile(filepath.Join(out, test.path))
			if err != nil || !bytes.Equal(raw, test.want) || digestBytes(raw) != result.Files[0].SHA256 {
				t.Fatalf("wrong payload bytes: %v", err)
			}
		})
	}
}

func TestPullOutputPreservesDeclaredLUKS2Disk(t *testing.T) {
	root, object, payload := pullLUKS2DiskFixture(t)
	var active atomic.Bool
	var payloadGets atomic.Int64
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if active.Load() && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+object.BlobDigest) {
			payloadGets.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	reference, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	active.Store(true)
	parent, workspace := t.TempDir(), t.TempDir()
	out := filepath.Join(parent, "disk")
	result, err := PullOutput(t.Context(), reference, object.Name, out, workspace, options)
	if err != nil {
		t.Fatal(err)
	}
	assertEmptyPullDirectory(t, workspace)
	if result.Output.Kind != "disk-image" || result.Output.Disk == nil || result.Output.Disk.Encryption != "luks2" ||
		result.Output.Disk.EvidenceMode != sbom.DiskBuildProvenance || len(result.Files) != 1 ||
		result.Files[0].Path != object.Name+".qcow2" || result.Files[0].SHA256 != object.BlobDigest ||
		result.Files[0].Size != int64(len(payload)) || payloadGets.Load() != 1 {
		t.Fatalf("pull lost exact declared disk binding: %+v, payload requests=%d", result, payloadGets.Load())
	}
	got, err := os.ReadFile(filepath.Join(out, result.Files[0].Path))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("declared LUKS2 payload was transformed: %v", err)
	}
}

func TestPullDiskOutputRejectsEncryptionSubstitutionBeforePayload(t *testing.T) {
	root, original, _ := pullLUKS2DiskFixture(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	for _, test := range []struct{ name, encryption string }{
		{"missing declaration", ""},
		{"unsupported declaration", "luks"},
		{"substituted declaration", "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := original
			binding := *object.Output
			disk := *binding.Disk
			disk.Encryption = test.encryption
			binding.Disk, object.Output = &disk, &binding
			parent, workspace := t.TempDir(), t.TempDir()
			reference := strings.TrimPrefix(server.URL, "http://") + "/product/node-image@" + object.DescriptorDigest
			err := pullDiskOutput(t.Context(), reference, filepath.Join(parent, "disk"), root, workspace, object,
				RegistryOptions{PlainHTTP: true, Anonymous: true})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if requests.Load() != 0 {
				t.Fatal("invalid declaration triggered registry work")
			}
			assertEmptyPullDirectory(t, parent)
			assertEmptyPullDirectory(t, workspace)
		})
	}
}

// Opaque synthetic payload and producer-provenance documents exercise transport
// integrity only. They do not establish actual guest encryption or signatures.
func pullLUKS2DiskFixture(t *testing.T) (string, PublishedObject, []byte) {
	t.Helper()
	request, manifest := diskPublicationRequest(t, "amd64")
	input := &request.Objects[0]
	manifest = rewriteDiskFixture(t, input.Path, manifest, func(wrapper *ociManifest, _ *[]byte) {
		wrapper.Annotations[diskEncryptionAnnotation] = "luks2"
	})
	_, index, err := readIndex(t.Context(), input.Path)
	if err != nil {
		t.Fatal(err)
	}
	input.BlobDigest = digestBytes(index)
	input.Output.Disk.Encryption = "luks2"
	input.Output.Disk.EvidenceMode = sbom.DiskBuildProvenance
	output := input.Output
	request.Release.ContentSubject, err = ContentDigest([]Output{{Name: input.Name, Kind: output.Format, Digest: manifest,
		BuildDigest: output.BuildDigest, Components: output.Components, Disk: output.Disk}})
	if err != nil {
		t.Fatal(err)
	}
	request.Objects = request.Objects[:1]
	for _, subject := range output.BuildSubjects {
		bom, err := sbom.NewSubject(output.BuildDigest, subject.Component, subject.Digest)
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.ForDiskBuildProvenance(bom, subject.Digest)
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		request.Objects = append(request.Objects, ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom",
			ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw,
			Evidence: &EvidenceBinding{Outputs: []string{"node-image"}, Component: subject.Component, Stage: "build-output", SubjectKind: "file", SubjectDigest: subject.Digest}})
	}
	root := filepath.Join(t.TempDir(), "release")
	if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Output == nil {
			continue
		}
		payload, err := os.ReadFile(fixtureBlobPath(root, object.BlobDigest))
		if err != nil {
			t.Fatal(err)
		}
		return root, object, payload
	}
	t.Fatal("LUKS2 fixture has no disk output")
	return "", PublishedObject{}, nil
}

func TestPullDirectoryFailureCleansNestedStage(t *testing.T) {
	for _, link := range []bool{false, true} {
		t.Run(map[bool]string{false: "populate failure", true: "symlink rejected"}[link], func(t *testing.T) {
			parent := t.TempDir()
			out := filepath.Join(parent, "output")
			sentinel := errors.New("populate stopped")
			err := publishPullDirectory(t.Context(), out, func(stage *os.File) error {
				child, err := createImageIndexDirectory(stage, "nested")
				if err != nil {
					return err
				}
				defer child.Close()
				if link {
					return unix.Symlinkat("missing", int(stage.Fd()), "link")
				}
				if err := writeImageIndexFile(t.Context(), child, "payload", func(w io.Writer) error { _, err := io.WriteString(w, "partial"); return err }); err != nil {
					return err
				}
				return sentinel
			})
			if link {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("lost populate error: %v", err)
			}
			assertEmptyPullDirectory(t, parent)
		})
	}
}

func TestPullDirectoryConflictCleansReadOnlyTree(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(parent, "existing")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	err := publishPullDirectory(t.Context(), out, func(stage *os.File) error {
		child, err := createImageIndexDirectory(stage, "read-only")
		if err != nil {
			return err
		}
		defer child.Close()
		if err := writeImageIndexFile(t.Context(), child, "payload", func(w io.Writer) error { _, err := io.WriteString(w, "data"); return err }); err != nil {
			return err
		}
		if err := child.Chmod(0o555); err != nil {
			return err
		}
		return stage.Chmod(0o555)
	})
	assertRegistryCode(t, err, e.CodeImmutableViolation)
	assertEmptyPullDirectory(t, out)
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "existing" {
		t.Fatalf("read-only private stage survived rollback: %v, %v", entries, err)
	}
}
