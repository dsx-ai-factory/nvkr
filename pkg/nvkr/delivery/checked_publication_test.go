// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestCheckedPublicationReleasesPreviousPayloadBeforeNext(t *testing.T) {
	fixture := pullExecutableFixture(t)
	workspace := t.TempDir()
	var active, diskFetched, overlap atomic.Bool
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if active.Load() && r.Method == http.MethodGet {
			if strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.diskLayer) {
				diskFetched.Store(true)
			}
			if strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.layer) {
				err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
					if err == nil && !entry.IsDir() && entry.Name() == strings.TrimPrefix(fixture.diskLayer, "sha256:") {
						overlap.Store(true)
					}
					return err
				})
				if err != nil {
					t.Error(err)
				}
				if overlap.Load() {
					http.Error(w, "previous payload still consumes bounded scratch", http.StatusInsufficientStorage)
					return
				}
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	reference, err := Push(t.Context(), fixture.root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	active.Store(true)
	checked, err := PullCheckedPublication(t.Context(), reference, workspace, options)
	if checked != nil {
		defer checked.Close()
	}
	if err != nil || overlap.Load() || !diskFetched.Load() {
		t.Fatalf("complete sequential verification: prior payload retained=%v disk read=%v err=%v", overlap.Load(), diskFetched.Load(), err)
	}
	if len(checked.state.diskFiles) != 0 || len(checked.state.diskPayloads) != 0 {
		t.Fatal("checked publication retained an open disk snapshot")
	}
	for _, file := range checked.state.files {
		if file.Digest() == fixture.diskLayer {
			t.Fatal("checked publication retained disk bytes in an anonymous file")
		}
	}
	assertEmptyPullDirectory(t, workspace)
}

func TestCheckedPublicationPreservesFullFormatChecksAndExecutableBytes(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture func(*testing.T) string
	}{
		{"disk and executable", func(t *testing.T) string { return pullExecutableFixture(t).root }},
		{"Helm and executable", func(t *testing.T) string { return packagePullFixture(t, outputFormatHelm, "exact").root }},
		{"APT and executable", func(t *testing.T) string { return packagePullFixture(t, "apt-repository", "exact").root }},
		{"native image", func(t *testing.T) string {
			root, _, _ := heldNativeImageFixture(t, types.OCIManifestSchema1)
			return root
		}},
		{"aggregate image", func(t *testing.T) string { root, _, _ := registryImageIndexFixture(t); return root }},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared := test.fixture(t)
			root := filepath.Join(t.TempDir(), "attested")
			built, err := AttestPreparedEvidence(t.Context(), prepared, root, func(_ context.Context, subject string, raw []byte) ([]byte, error) {
				return imageIndexFixtureAttestation(subject, raw)
			})
			if err != nil {
				t.Fatal(err)
			}
			full, err := openDiskPublicationFixture(t.Context(), root, built.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer full.Close()
			server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			reference, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			checked, err := PullCheckedPublication(t.Context(), reference, workspace, options)
			if err != nil {
				t.Fatal(err)
			}
			defer checked.Close()
			assertEmptyPullDirectory(t, workspace)
			want, err := full.Description(reference)
			if err != nil {
				t.Fatal(err)
			}
			got, err := checked.Description(reference)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("checked description differs: got=%+v want=%+v err=%v", got, want, err)
			}
			if err := checked.RequireAttestedEvidence(); err != nil {
				t.Fatal(err)
			}
			var evidence int
			if err := checked.VisitEvidence(t.Context(), func(_ context.Context, _ EvidenceBinding, raw []byte) error {
				evidence++
				if len(raw) == 0 {
					t.Error("empty evidence")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if evidence != len(want.Evidence) {
				t.Fatalf("evidence count=%d want=%d", evidence, len(want.Evidence))
			}
			server.Close()
			if err := os.Rename(root, root+"-replaced"); err != nil {
				t.Fatal(err)
			}
			for _, output := range want.Outputs {
				if output.Kind != "executable" {
					continue
				}
				bundle, err := checked.ExecutableBundle(output.Name)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range bundle.Entries {
					var raw bytes.Buffer
					written, err := checked.WriteExecutableMember(t.Context(), output.Name, entry.Name, &raw)
					if err != nil || written != entry.Size || digestBytes(raw.Bytes()) != entry.SHA256 {
						t.Fatalf("retained executable changed: %d %v", written, err)
					}
				}
			}
			files := append([]*heldfile.File(nil), checked.state.files...)
			copyOf := *checked
			if err := copyOf.Close(); err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				if file.OSFile() != nil {
					t.Fatal("closed owner leaked a retained file")
				}
			}
			_, err = checked.Description(reference)
			assertRegistryCode(t, err, e.CodeGateUnmet)
		})
	}
}

func checkedLateDiskFixture(t *testing.T) (string, string) {
	t.Helper()
	request := releasePublicationRequestFixture(t, "linux", "amd64")
	disk, _ := diskPublicationRequest(t, "arm64")
	request.Release.PlanDigest = disk.Release.PlanDigest
	for i := range disk.Objects {
		if disk.Objects[i].Output != nil {
			disk.Objects[i].Name = "z-node-image"
			disk.Objects[i].Output.Repository = "z-node-image"
		}
		if disk.Objects[i].Evidence != nil {
			disk.Objects[i].Evidence.Outputs = []string{"z-node-image"}
		}
	}
	request.Objects = append(request.Objects, disk.Objects...)
	var outputs []Output
	for _, object := range request.Objects {
		if object.Output == nil {
			continue
		}
		descriptor, err := PayloadDescriptor(t.Context(), object.Path)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, Output{Name: object.Name, Kind: object.Output.Format, Digest: descriptor.Digest, BuildDigest: object.Output.BuildDigest, Components: object.Output.Components, Disk: object.Output.Disk})
	}
	var err error
	request.Release.ContentSubject, err = ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "late-disk")
	if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
		t.Fatal(err)
	}
	payload := disk.Objects[0].Output.Disk.PayloadDigest
	return root, payload
}

func TestCheckedPublicationLateFailureReleasesRetainedFiles(t *testing.T) {
	root, payload := checkedLateDiskFixture(t)
	for _, mode := range []string{"corruption", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			var active, reached atomic.Bool
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if active.Load() && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+payload) {
					reached.Store(true)
					if mode == "cancellation" {
						cancel()
						<-r.Context().Done()
						return
					}
					raw, readErr := os.ReadFile(fixtureBlobPath(root, payload))
					if readErr != nil {
						t.Error(readErr)
						w.WriteHeader(500)
						return
					}
					raw[0] ^= 1
					_, _ = w.Write(raw)
					return
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
			workspace := t.TempDir()
			operation := &checkedPublicationPull{}
			checked, err := operation.run(ctx, reference, workspace, options)
			code := e.CodeFetchFailed // The registry reader rejects its own streamed checksum.
			if mode == "cancellation" {
				code = e.CodeTimeout
			}
			assertRegistryCode(t, err, code)
			if checked != nil || !reached.Load() {
				t.Fatalf("late failure not reached or issued owner: %v %v", checked, err)
			}
			if operation.state == nil || !operation.state.closed || len(operation.state.files) != 0 || len(operation.state.executables) != 0 {
				t.Fatal("late failure retained control or executable files")
			}
			if len(operation.state.outputs) != 1 || operation.state.outputs[0].Name != "tools" {
				t.Fatalf("failure did not follow successful executable retention: %+v", operation.state.outputs)
			}
			assertEmptyPullDirectory(t, workspace)
		})
	}
}
