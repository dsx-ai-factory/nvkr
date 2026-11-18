// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDiskLayerRetention(t *testing.T) {
	scratch := t.TempDir()
	raw := bytes.Repeat([]byte("runtime"), 1024)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	source := diskLayerFixture(t, scratch, raw)
	layer, err := captureDiskLayer(t.Context(), source, digest, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = layer.Close() })
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 1 || entries[0].Name() != "layer" {
		t.Fatalf("retention created a second copy: %v, %v", entries, err)
	}
	info, err := layer.file.Stat()
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("retained permissions: %v, %v", info, err)
	}
	original, err := source.Stat()
	if err != nil || !os.SameFile(original, info) {
		t.Fatalf("retention replaced the original tar inode: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := layer.file.WriteAt([]byte("changed"), 0); err == nil {
		t.Fatal("retained a writable descriptor")
	}
	actual := make([]byte, len(raw))
	if _, err := layer.ReadAt(actual, 0); err != nil || !bytes.Equal(raw, actual) {
		t.Fatalf("retained bytes: %v", err)
	}
	if layer.Digest() != digest || layer.Size() != int64(len(raw)) {
		t.Fatal("lost exact input binding")
	}
	if err := layer.AssertUnchangedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := layer.Close(); err != nil {
		t.Fatal(err)
	}
	requireStageCode(t, layer.AssertUnchangedContext(t.Context()), e.CodeGateUnmet)
}

func TestDiskLayerRejectsChangedInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    []byte
		size   int64
		digest string
	}{
		{"digest", []byte("abc"), 3, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("def")))},
		{"short", []byte("ab"), 3, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("abc")))},
		{"long", []byte("abcd"), 3, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("abc")))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scratch := t.TempDir()
			layer, err := captureDiskLayer(t.Context(), diskLayerFixture(t, scratch, tc.raw), tc.digest, tc.size)
			if layer != nil {
				_ = layer.Close()
				t.Fatal("rejected input returned a layer")
			}
			requireStageCode(t, err, e.CodeVerifyFailed)
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 1 {
				t.Fatalf("failed capture changed the input inventory: %v %v", entries, err)
			}
		})
	}
}

func TestDiskLayerCanceledCapture(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := captureDiskLayer(ctx, nil, "", 3)
	requireStageCode(t, err, e.CodeTimeout)
}

func TestDiskLayerRequiresDiskScratch(t *testing.T) {
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("no memory-backed scratch fixture")
	}
	directory, _, err := securepath.OpenDirectory("/dev/shm")
	if err != nil {
		t.Skip(err)
	}
	defer directory.Close()
	if _, err := securepath.AvailableDiskBackedBytes(t.Context(), directory); err == nil {
		t.Skip("fixture is not memory-backed")
	}
	private, err := os.MkdirTemp("/dev/shm", "nvkr-layer-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(private) })
	_, err = captureDiskLayer(t.Context(), diskLayerFixture(t, private, []byte("abc")), "", 3)
	requireStageCode(t, err, e.CodeEngineCapability)
	held := layerFixture(t, nil, nil)
	_, err = Stage(t.Context(), held, "/dev/shm")
	requireStageCode(t, err, e.CodeEngineCapability)
}

// This source preserves its preflight hash but supplies different valid tar
// content during extraction. The staged root must never become available.
type changingLayer struct{ layerInput }

func (layer changingLayer) ReadAt(buffer []byte, offset int64) (int, error) {
	n, err := layer.layerInput.ReadAt(buffer, offset)
	if offset <= 1024 && offset+int64(n) > 1024 {
		buffer[1024-offset] ^= 1
	}
	return n, err
}
func TestStageRejectsChangedStream(t *testing.T) {
	held := layerFixture(t, []tar.Header{rootHeader(), {Name: "tool", Typeflag: tar.TypeReg, Mode: 0o444, Size: 7}}, nil)
	scratch := t.TempDir()
	tree, err := Stage(t.Context(), changingLayer{held}, scratch)
	if tree != nil {
		_ = tree.Close()
		t.Fatal("changed layer released a staged root")
	}
	requireStageCode(t, err, e.CodeVerifyFailed)
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected extraction leaked files: %v %v", entries, err)
	}
}

func TestDiskLayerRejectsMutationThroughAnotherDescriptor(t *testing.T) {
	raw := []byte("runtime")
	writable, err := os.CreateTemp(t.TempDir(), "layer")
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if _, err := writable.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writable.Chmod(0o400); err != nil {
		t.Fatal(err)
	}
	readonly, err := os.Open(writable.Name())
	if err != nil {
		t.Fatal(err)
	}
	layer := &Layer{file: readonly, size: int64(len(raw)), digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), mode: 0o400}
	defer layer.Close()
	if err := os.Remove(writable.Name()); err != nil {
		t.Fatal(err)
	}
	if err := layer.AssertUnchangedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Simulate a privileged writer changing the retained original inode.
	// Mutation must revoke the layer before any staged root is released.
	if _, err := writable.WriteAt([]byte("changed"), 0); err != nil {
		t.Fatal(err)
	}
	requireStageCode(t, layer.AssertUnchangedContext(t.Context()), e.CodeVerifyFailed)
}

func diskLayerFixture(t *testing.T, directory string, raw []byte) *os.File {
	t.Helper()
	path := filepath.Join(directory, "layer")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestDiskLayerRejectsWritableDescriptorAndPermissions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		writable bool
		mode     os.FileMode
	}{
		{name: "writable descriptor", writable: true, mode: 0o644},
		{name: "other writable inode", mode: 0o666},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := diskLayerFixture(t, t.TempDir(), []byte("runtime"))
			if err := source.Chmod(tc.mode); err != nil {
				t.Fatal(err)
			}
			if tc.writable {
				writable, err := os.OpenFile(source.Name(), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer writable.Close()
				source = writable
			}
			layer, err := captureDiskLayer(t.Context(), source, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("runtime"))), 7)
			if layer != nil {
				_ = layer.Close()
				t.Fatal("unsafe layer accepted")
			}
			requireStageCode(t, err, e.CodeVerifyFailed)
		})
	}
}
