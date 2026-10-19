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
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
)

func registryDiskFixture(t *testing.T, arch string) (string, PublishedObject, []byte) {
	t.Helper()
	request, _ := diskPublicationRequest(t, arch)
	root := filepath.Join(t.TempDir(), "publication")
	if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Output != nil {
			raw, err := os.ReadFile(fixtureBlobPath(root, object.Output.Disk.PayloadDigest))
			if err != nil {
				t.Fatal(err)
			}
			return root, object, raw
		}
	}
	t.Fatal("fixture has no disk output")
	return "", PublishedObject{}, nil
}

func TestRegistryDiskHoldsOriginalInodeAndReplaysExactBodies(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			root, object, payload := registryDiskFixture(t, arch)
			original, err := os.Stat(fixtureBlobPath(root, object.Output.Disk.PayloadDigest))
			if err != nil {
				t.Fatal(err)
			}
			scratch := t.TempDir()
			t.Setenv("TMPDIR", scratch)
			image, owner, err := registryDiskImage(t.Context(), root, object)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			held := owner.disk.owner.file
			observed, err := held.Stat()
			if err != nil || !os.SameFile(original, observed) {
				t.Fatal("disk upload copied or replaced the original inode")
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatal("disk upload created a second payload workspace")
			}
			raw, err := image.RawManifest()
			if err != nil {
				t.Fatal(err)
			}
			raw[0] ^= 1
			again, err := image.RawManifest()
			if err != nil || digestBytes(again) != object.DescriptorDigest {
				t.Fatal("caller mutated the captured manifest")
			}
			layers, err := image.Layers()
			if err != nil || len(layers) != 1 {
				t.Fatalf("disk layer projection: %v", err)
			}
			first, err := layers[0].Compressed()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(first, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				reader, err := layers[0].Compressed()
				if err != nil {
					t.Fatal(err)
				}
				got, readErr := io.ReadAll(reader)
				if closeErr := reader.Close(); readErr != nil || closeErr != nil || !bytes.Equal(got, payload) {
					t.Fatalf("fresh retry body changed exact bytes: %v %v", readErr, closeErr)
				}
			}
			if err := owner.verify(); err != nil {
				t.Fatal(err)
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := held.Stat(); err == nil {
				t.Fatal("disk upload retained its payload descriptor")
			}
			_, err = layers[0].Compressed()
			assertRegistryCode(t, err, e.CodeGateUnmet)
		})
	}
}

func TestRegistryDiskRejectsSourceMutationWithoutPathReopening(t *testing.T) {
	for _, change := range []string{"path replacement", "same inode", "restored mtime", "growth", "shrink"} {
		t.Run(change, func(t *testing.T) {
			root, object, payload := registryDiskFixture(t, "amd64")
			_, owner, err := registryDiskImage(t.Context(), root, object)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			path := fixtureBlobPath(root, object.Output.Disk.PayloadDigest)
			changed := bytes.Clone(payload)
			switch change {
			case "path replacement":
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				changed[0] ^= 1
			case "same inode", "restored mtime":
				changed[0] ^= 1
			case "growth":
				changed = append(changed, 1)
			case "shrink":
				changed = changed[:len(changed)-1]
			}
			if err := os.WriteFile(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			if change == "restored mtime" {
				if err := os.Chtimes(path, owner.info.ModTime(), owner.info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			err = owner.verify()
			if change == "path replacement" {
				if err != nil {
					t.Fatalf("pathname replacement redirected the held inode: %v", err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestRegistryDiskRejectsRestoredMutationAndCancellation(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "restored corruption", true: "canceled read"}[cancelRead], func(t *testing.T) {
			root, object, payload := registryDiskFixture(t, "amd64")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, owner, err := registryDiskImage(ctx, root, object)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			reader, err := owner.body()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reader.Close() }()
			if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			path := fixtureBlobPath(root, object.Output.Disk.PayloadDigest)
			if cancelRead {
				cancel()
			} else {
				changed := bytes.Clone(payload)
				changed[len(changed)-1] ^= 1
				if err := os.WriteFile(path, changed, owner.info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, owner.info.ModTime(), owner.info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			_, err = io.Copy(io.Discard, reader)
			code := e.CodeVerifyFailed
			if cancelRead {
				code = e.CodeTimeout
			}
			assertRegistryCode(t, err, code)
			if !cancelRead {
				if err := os.WriteFile(path, payload, owner.info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, owner.info.ModTime(), owner.info.ModTime()); err != nil {
					t.Fatal(err)
				}
				_, err = owner.body()
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestRegistryDiskDedupStillAuthenticatesSourceBeforeReleaseRoot(t *testing.T) {
	root, object, payload := registryDiskFixture(t, "amd64")
	path := fixtureBlobPath(root, object.Output.Disk.PayloadDigest)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var armed, changed atomic.Bool
	var roots atomic.Int32
	failures := make(chan error, 1)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v2/candidate/releases/manifests/") {
			roots.Add(1)
		}
		if armed.Load() && r.Method == http.MethodHead && r.URL.Path == "/v2/candidate/node-image/manifests/"+object.DescriptorDigest && changed.CompareAndSwap(false, true) {
			mutated := bytes.Clone(payload)
			mutated[0] ^= 1
			mutationErr := os.WriteFile(path, mutated, info.Mode().Perm())
			if mutationErr == nil {
				mutationErr = os.Chtimes(path, info.ModTime(), info.ModTime())
			}
			if mutationErr != nil {
				failures <- mutationErr
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	base := strings.TrimPrefix(server.URL, "http://") + "/candidate"
	ref, err := name.NewDigest(base+"/node-image@"+object.DescriptorDigest, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	if err := pushRegistryDisk(t.Context(), root, object, ref, options); err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	_, err = Push(t.Context(), root, base+"/releases", options)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
	if !changed.Load() || roots.Load() != 0 {
		t.Fatal("deduplicated output bypassed local bytes or advanced the release root")
	}
}
