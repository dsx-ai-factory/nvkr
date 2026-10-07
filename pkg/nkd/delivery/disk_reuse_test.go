// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestOpenSingleDiskArtifactAuthenticatesCompleteClosure(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, scenario := range []string{"complete", "changed payload", "extra blob", "duplicate manifest", "wrong platform", "budget", "canceled"} {
			t.Run(platform+"/"+scenario, func(t *testing.T) {
				root, payload, pin := diskFixture(t, platform)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				budget := int64(len(payload))
				expectedPlatform := platform
				switch scenario {
				case "changed payload":
					payloadPin := digestBytes(payload)
					payload[0] ^= 1
					// Keep the declared payload pin unchanged.
					if err := os.WriteFile(diskBlobPath(root, payloadPin), payload, 0o600); err != nil {
						t.Fatal(err)
					}
				case "extra blob":
					if err := os.WriteFile(diskBlobPath(root, digestBytes([]byte("orphan"))), []byte("orphan"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "duplicate manifest":
					index, _, err := readIndex(ctx, root)
					if err != nil {
						t.Fatal(err)
					}
					index.Manifests = append(index.Manifests, index.Manifests[0])
					if _, err := (layoutWriter{root: root}).writeIndex(index); err != nil {
						t.Fatal(err)
					}
				case "wrong platform":
					if platform == "linux/amd64" {
						expectedPlatform = "linux/arm64"
					} else {
						expectedPlatform = "linux/amd64"
					}
				case "budget":
					budget--
				case "canceled":
					cancel()
				}
				owner, err := OpenSingleDiskArtifact(ctx, root, expectedPlatform, budget)
				if scenario != "complete" {
					code := e.CodeVerifyFailed
					if scenario == "canceled" {
						code = e.CodeTimeout
					}
					assertRegistryCode(t, err, code)
					if owner != nil {
						_ = owner.Close()
						t.Fatal("failed verification returned an owner")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = owner.Close() }()
				if owner.Inspection().ManifestDigest != pin {
					t.Fatal("reader selected a different manifest")
				}
				var copied bytes.Buffer
				if _, err := owner.WriteToContext(ctx, &copied); err != nil || !bytes.Equal(copied.Bytes(), payload) {
					t.Fatalf("exact held payload: %v", err)
				}
			})
		}
	}
}

func TestDescriptorWalkRetainsDiskInodeAndRechecksCopy(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, scenario := range []string{"replaced path", "mutated inode", "closed owner", "canceled"} {
			t.Run(platform+"/"+scenario, func(t *testing.T) {
				root, payload, _ := diskFixture(t, platform)
				index, _, err := readIndex(t.Context(), root)
				if err != nil {
					t.Fatal(err)
				}
				owners := map[string]*DiskArtifact{}
				visited := map[string]bool{}
				if err := verifyDescriptorHoldingDisks(t.Context(), root, index.Manifests[0], visited, nil, owners); err != nil {
					t.Fatal(err)
				}
				owner := owners[digestBytes(payload)]
				if len(owners) != 1 || owner == nil || len(visited) != 3 {
					t.Fatalf("walk did not retain the complete verified disk: %d, %d", len(owners), len(visited))
				}
				defer func() { _ = owner.Close() }()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				path := diskBlobPath(root, digestBytes(payload))
				switch scenario {
				case "replaced path":
					if err := os.Rename(path, path+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, bytes.Repeat([]byte("x"), len(payload)), 0o600); err != nil {
						t.Fatal(err)
					}
				case "mutated inode":
					if err := os.WriteFile(path, bytes.Repeat([]byte("x"), len(payload)), 0o600); err != nil {
						t.Fatal(err)
					}
				case "closed owner":
					if err := owner.Close(); err != nil {
						t.Fatal(err)
					}
				case "canceled":
					cancel()
				}
				stage := t.TempDir()
				err = (layoutWriter{root: stage}).copyDiskPayload(ctx, owner)
				if scenario != "replaced path" {
					code := e.CodeVerifyFailed
					if scenario == "canceled" {
						code = e.CodeTimeout
					}
					assertRegistryCode(t, err, code)
					if scenario == "closed owner" && !strings.Contains(err.Error(), "disk artifact is closed") {
						t.Fatalf("copy did not reject the closed owner: %v", err)
					}
					return
				}
				copied, readErr := os.ReadFile(diskBlobPath(stage, digestBytes(payload)))
				if err != nil || readErr != nil || !bytes.Equal(copied, payload) {
					t.Fatalf("copy did not consume the original verified inode: %v, %v", err, readErr)
				}
			})
		}
	}
}

func BenchmarkDiskSelection(b *testing.B) {
	// This measures the producer reader only, not a real QCOW2 build or acceptance.
	payload := bytes.Repeat([]byte("opaque disk"), 1<<20)
	source := filepath.Join(b.TempDir(), "source")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		b.Fatal(err)
	}
	root := filepath.Join(b.TempDir(), "layout")
	if _, err := BuildDiskArtifact(b.Context(), root, DiskArtifactRequest{Path: source, Digest: digestBytes(payload), Size: int64(len(payload)),
		Platform: "linux/amd64", Format: "qcow2", Encryption: "none", ArtifactType: MediaDiskImage}); err != nil {
		b.Fatal(err)
	}
	for _, reader := range []string{"descriptor then open", "single held selection"} {
		b.Run(reader, func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				var owner *DiskArtifact
				var err error
				if reader == "descriptor then open" {
					descriptor, selectErr := PayloadDescriptor(b.Context(), root)
					if selectErr != nil {
						b.Fatal(selectErr)
					}
					owner, err = OpenDiskArtifact(b.Context(), root, descriptor.Digest, "linux/amd64", int64(len(payload)))
				} else {
					owner, err = OpenSingleDiskArtifact(b.Context(), root, "linux/amd64", int64(len(payload)))
				}
				if err != nil {
					b.Fatal(err)
				}
				if err := owner.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
