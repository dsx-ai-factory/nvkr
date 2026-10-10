// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package assemble

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func tempBinary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(p, []byte("\x7fELF fake binary contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDebDeterministic(t *testing.T) {
	bin := tempBinary(t)
	dir := t.TempDir()
	d1, err := Deb(bin, "kubectl", "1.32.0", 1733939955, "amd64", filepath.Join(dir, "a.deb"))
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := Deb(bin, "kubectl", "1.32.0", 1733939955, "amd64", filepath.Join(dir, "b.deb"))
	if d1 != d2 {
		t.Fatalf(".deb not reproducible: %s vs %s", d1, d2)
	}
	// SOURCE_DATE_EPOCH participates in the bytes.
	d3, _ := Deb(bin, "kubectl", "1.32.0", 1, "amd64", filepath.Join(dir, "c.deb"))
	if d3 == d1 {
		t.Fatal(".deb digest did not change with SOURCE_DATE_EPOCH")
	}
	// arch participates: an arm64 .deb differs from the amd64 one (multi-arch, model B).
	dArm, _ := Deb(bin, "kubectl", "1.32.0", 1733939955, "arm64", filepath.Join(dir, "arm.deb"))
	if dArm == d1 {
		t.Fatal(".deb digest did not change with arch")
	}
}

// The canonical disk boundary is the RAW logical stream (NVKR v1 §8.3.1): its digest
// is a pure function of the bytes, and the per-block manifest's aggregate equals it
// (the diagnosable granularity of §8.4.2).
func TestLogicalDiskDigestAndBlockManifest(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "disk.raw")
	// Two full bounded blocks plus a short tail.
	content := bytes.Repeat([]byte("n"), 2*MinDiskBlockSizeBytes+2)
	if err := os.WriteFile(raw, content, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := NewDiskManifestContext(t.Context(), DiskKindRAW, raw, MinDiskBlockSizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.VirtualSizeBytes != int64(len(content)) {
		t.Fatalf("size = %d, want %d", manifest.VirtualSizeBytes, len(content))
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	if manifest.LogicalDigest != digest {
		t.Fatalf("logical digest = %s, want %s", manifest.LogicalDigest, digest)
	}
	bm := manifest.BlockManifest
	if len(bm.Blocks) != 3 {
		t.Fatalf("expected two full blocks and a tail, got %d", len(bm.Blocks))
	}
	if bm.Aggregate != digest {
		t.Fatalf("block-manifest aggregate %s must equal the logical-disk digest %s", bm.Aggregate, digest)
	}
	if _, err := NewDiskManifestContext(t.Context(), DiskKindRAW, raw, 0); code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatal("block size 0 must error")
	}
}

func TestVerifyLayoutDetectsCorruption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "img")
	if _, err := OCIImage(tempBinary(t), "kubectl", 1733939955, "amd64", dir); err != nil {
		t.Fatal(err)
	}
	if err := VerifyLayoutContext(context.Background(), dir); err != nil {
		t.Fatalf("intact layout must verify: %v", err)
	}
	// corrupt one blob (e.g. the layer holding the binary) -> must fail.
	blobs := filepath.Join(dir, "blobs", "sha256")
	ents, _ := os.ReadDir(blobs)
	if err := os.WriteFile(filepath.Join(blobs, ents[0].Name()), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyLayoutContext(context.Background(), dir); err == nil {
		t.Fatal("corrupted blob must fail VerifyLayout")
	}
}

func TestVerifyLayoutContextRejectsCancellationAndNonRegularBlobs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	missing := filepath.Join(t.TempDir(), "must-not-be-opened")
	if err := VerifyLayoutContext(ctx, missing); code(err) != nvkrerr.CodeTimeout || !nvkrerr.Retryable(err) {
		t.Fatalf("pre-canceled layout verification error = %v, want retryable timeout", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "symlink",
			mutate: func(t *testing.T, blob string) {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Rename(blob, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, blob); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "fifo",
			mutate: func(t *testing.T, blob string) {
				if err := os.Remove(blob); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(blob, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "img")
			if _, err := OCIImage(tempBinary(t), "kubectl", 1733939955, "amd64", dir); err != nil {
				t.Fatal(err)
			}
			blobs := filepath.Join(dir, "blobs", "sha256")
			entries, err := os.ReadDir(blobs)
			if err != nil || len(entries) == 0 {
				t.Fatalf("listing fixture blobs: %v", err)
			}
			test.mutate(t, filepath.Join(blobs, entries[0].Name()))
			if err := VerifyLayoutContext(context.Background(), dir); code(err) != nvkrerr.CodeVerifyFailed {
				t.Fatalf("non-regular OCI blob error = %v, want verify-failed", err)
			}
		})
	}
}

func TestOCIImageDeterministicAndReal(t *testing.T) {
	bin := tempBinary(t)
	d1, err := OCIImage(bin, "kubectl", 1733939955, "amd64", filepath.Join(t.TempDir(), "img"))
	if err != nil {
		t.Fatal(err)
	}
	out2 := filepath.Join(t.TempDir(), "img")
	d2, err := OCIImage(bin, "kubectl", 1733939955, "amd64", out2)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("OCI manifest digest not reproducible: %s vs %s", d1, d2)
	}
	// arch participates: an arm64 image config differs, so its manifest digest differs.
	dArm, err := OCIImage(bin, "kubectl", 1733939955, "arm64", filepath.Join(t.TempDir(), "img-arm"))
	if err != nil {
		t.Fatal(err)
	}
	if dArm == d1 {
		t.Fatal("OCI manifest digest did not change with arch")
	}
	// a real, inspectable OCI layout was written, with the manifest blob present.
	for _, f := range []string{"oci-layout", "index.json", filepath.Join("blobs", "sha256", d1[len("sha256:"):])} {
		if _, err := os.Stat(filepath.Join(out2, f)); err != nil {
			t.Errorf("expected OCI layout file %s: %v", f, err)
		}
	}
}
