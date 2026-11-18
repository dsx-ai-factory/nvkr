// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/rootfs"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func mkosiTarFixture(t *testing.T, contents string) (string, mkosi.Invocation, []byte) {
	t.Helper()
	return mkosiNamedTarFixture(t, "payload", contents)
}

func mkosiNamedTarFixture(t *testing.T, name, contents string) (string, mkosi.Invocation, []byte) {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{Name: "./", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o444, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "runtime.tar"), raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return work, mkosi.Invocation{Format: "tar", Output: "/work/runtime.tar"}, raw.Bytes()
}

func TestMkosiTarWrappingPreservesBytesAndIdentity(t *testing.T) {
	var baseline string
	for _, tc := range []struct {
		name, platform, contents, filename string
		equal                              bool
	}{
		{"first", "linux/amd64", "exact tar payload", "payload", true},
		{"fresh directory", "linux/amd64", "exact tar payload", "payload", true},
		{"different platform", "linux/arm64", "exact tar payload", "payload", false},
		{"different bytes", "linux/amd64", "other tar payload", "payload", false},
		{"systemd escaped name", "linux/amd64", "exact tar payload", `system-systemd\x2dcryptsetup.slice`, false},
		{"literal backslash dots", "linux/amd64", "exact tar payload", `..\escape`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work, operation, original := mkosiNamedTarFixture(t, tc.filename, tc.contents)
			// Host timestamps do not enter the config or modify the tar payload.
			if err := os.Chtimes(filepath.Join(work, "runtime.tar"), time.Now(), time.Now()); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(work, "image")
			if err := wrapMkosiTar(t.Context(), work, operation, tc.platform, destination); err != nil {
				t.Fatal(err)
			}
			indexRaw, err := os.ReadFile(filepath.Join(destination, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			var index v1.IndexManifest
			if err := json.Unmarshal(indexRaw, &index); err != nil || len(index.Manifests) != 1 {
				t.Fatalf("invalid image index: %v", err)
			}
			subject := index.Manifests[0].Digest.String()
			tree, err := ociclosure.Snapshot(t.Context(), destination)
			if err != nil {
				t.Fatal(err)
			}
			if baseline == "" {
				baseline = tree.Digest()
			}
			if (tree.Digest() == baseline) != tc.equal {
				t.Fatalf("complete OCI closure equality = %v, want %v", tree.Digest() == baseline, tc.equal)
			}
			manifestRaw, err := tree.ReadRegular(t.Context(), "blobs/sha256/"+index.Manifests[0].Digest.Hex, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			var manifest v1.Manifest
			if err := json.Unmarshal(manifestRaw, &manifest); err != nil || len(manifest.Layers) != 1 ||
				manifest.Layers[0].MediaType != types.OCIUncompressedLayer || manifest.Layers[0].Digest.String() != FileDigest(original) ||
				manifest.Layers[0].Size != int64(len(original)) {
				t.Fatalf("tar descriptor changed: %+v, %v", manifest.Layers, err)
			}
			stored, err := tree.ReadRegular(t.Context(), "blobs/sha256/"+manifest.Layers[0].Digest.Hex, int64(len(original)))
			if err != nil || !bytes.Equal(stored, original) {
				t.Fatalf("tar bytes were repackaged: %v", err)
			}
			layer, err := rootfs.CaptureLayer(t.Context(), destination, subject, tc.platform, t.TempDir())
			if err != nil {
				t.Fatalf("capture wrapped tar: %v; index %s", err, indexRaw)
			}
			defer layer.Close()
			staged, err := rootfs.Stage(t.Context(), layer, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer staged.Close()
			directory, err := staged.Directory()
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			digest, _, mode, err := hashMkosiRootFile(t.Context(), directory, tc.filename)
			if err != nil || digest != FileDigest([]byte(tc.contents)) || mode != 0o444 {
				t.Fatalf("wrapped tar failed existing runtime staging: %s %v %v", digest, mode, err)
			}
		})
	}
}

func TestMkosiTarWrappingRejectsBeforeOutput(t *testing.T) {
	for _, scenario := range []string{"canceled", "unsupported platform", "wrong format", "symlink", "empty", "oversized", "occupied output"} {
		t.Run(scenario, func(t *testing.T) {
			work, operation, _ := mkosiTarFixture(t, "payload")
			destination := filepath.Join(work, "image")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			platform := "linux/amd64"
			code := e.CodeVerifyFailed
			switch scenario {
			case "canceled":
				cancel()
				code = e.CodeTimeout
			case "unsupported platform":
				platform, code = "linux/s390x", e.CodeEngineCapability
			case "wrong format":
				operation.Format, code = "oci", e.CodeEngineCapability
			case "symlink":
				if err := os.Symlink(filepath.Join(work, "runtime.tar"), filepath.Join(work, "linked.tar")); err != nil {
					t.Fatal(err)
				}
				operation.Output = "/work/linked.tar"
			case "empty", "oversized":
				size := int64(0)
				if scenario == "oversized" {
					size = rootfs.MaxLayerBytes + 1
				}
				if err := os.Truncate(filepath.Join(work, "runtime.tar"), size); err != nil {
					t.Fatal(err)
				}
			case "occupied output":
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				code = e.CodeBuildFailed
			}
			err := wrapMkosiTar(ctx, work, operation, platform, destination)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != code {
				t.Fatalf("got %v; want %s", err, code)
			}
			if scenario == "occupied output" {
				entries, err := os.ReadDir(destination)
				if err != nil || len(entries) != 0 {
					t.Fatalf("occupied output changed: %v", err)
				}
			} else if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("rejected wrapping wrote output")
			}
		})
	}
}

func TestMkosiTarStreamUsesHeldDescriptorAndCancellation(t *testing.T) {
	work, _, original := mkosiTarFixture(t, "payload")
	file, err := os.Open(filepath.Join(work, "runtime.tar"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	layer := &mkosiTarLayer{ctx: ctx, file: file, size: int64(len(original))}
	if err := os.Rename(file.Name(), file.Name()+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.Name(), []byte("substitute"), 0o600); err != nil {
		t.Fatal(err)
	}
	stream, err := layer.Compressed()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var got bytes.Buffer
	if _, err := got.ReadFrom(stream); err != nil || !bytes.Equal(got.Bytes(), original) {
		t.Fatalf("stream reopened substituted pathname: %v", err)
	}
	cancel()
	if _, err := layer.Uncompressed(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stream opened: %v", err)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("existing stream ignored cancellation: %v", err)
	}
}
