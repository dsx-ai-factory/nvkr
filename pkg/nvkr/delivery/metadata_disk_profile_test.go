// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestInspectMetadataEnforcesDiskBindingWithoutPayloads(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, arch)
			full := filepath.Join(t.TempDir(), "full")
			prepared, err := BuildPublicationRoot(t.Context(), full, request)
			if err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(t.TempDir(), "metadata")
			exported, err := ExportMetadata(t.Context(), full, base)
			if err != nil || exported.RootDigest != prepared.RootDigest {
				t.Fatalf("metadata export changed the valid fixture: %+v, %v", exported, err)
			}
			for _, test := range []struct {
				name string
				edit func(*PublishedObject)
			}{
				{"valid", nil},
				{"size contradiction", func(object *PublishedObject) { object.Output.Disk.Size++ }},
				{"platform contradiction", func(object *PublishedObject) {
					object.Arch = map[string]string{"amd64": "arm64", "arm64": "amd64"}[object.Arch]
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					root := filepath.Join(t.TempDir(), "metadata")
					if err := os.CopyFS(root, os.DirFS(base)); err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(fixtureBlobPath(root, request.Objects[0].Output.Disk.PayloadDigest)); !os.IsNotExist(err) {
						t.Fatalf("metadata fixture contains a disk payload: %v", err)
					}
					original, err := InspectMetadata(t.Context(), root)
					if err != nil {
						t.Fatal(err)
					}
					pin := original.RootDigest
					if test.edit != nil {
						// Rewrite every outer byte/semantic identity. These declarations
						// do not change the disk's SBOM subjects or evidence membership.
						pin = rewriteDiskPublicationRoot(t, root, original, func(publication *PublicationRoot) {
							for i := range publication.Objects {
								if publication.Objects[i].Output != nil {
									test.edit(&publication.Objects[i])
								}
							}
						})
						if pin == original.RootDigest {
							t.Fatal("fixture edit did not change the exact release root")
						}
						if err := verifyReleaseEvidence(t.Context(), root, original.Root); err != nil {
							t.Fatalf("fixture edit invalidated evidence instead of the disk binding: %v", err)
						}
					}
					t.Run("inspect", func(t *testing.T) {
						got, err := InspectMetadata(t.Context(), root)
						if test.edit == nil {
							if err != nil || got.RootDigest != pin || len(got.Root.Objects) != len(request.Objects) {
								t.Fatalf("valid disk metadata required payload bytes or changed identity: %+v, %v", got, err)
							}
							return
						}
						assertRegistryCode(t, err, e.CodeVerifyFailed)
						if !reflect.DeepEqual(got, Inspection{}) {
							t.Fatalf("contradictory disk binding returned metadata inspection: %+v", got)
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
							t.Fatalf("contradictory disk metadata reached publication: reference=%q requests=%d", reference, requests.Load())
						}
					})
				})
			}
		})
	}
}
