// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package assemble

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// ADR-14 §1: the unified DiskManifest anchors on the logical digest of the decoded RAW
// stream, carries a per-block manifest, and is deterministic across runs.
func TestDiskManifestDeterministic(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "node.raw")
	if err := os.WriteFile(raw, []byte("this-is-a-fake-raw-node-disk-stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	m1, err := NewDiskManifestContext(context.Background(), DiskKindRAW, raw, MinDiskBlockSizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if m1.BlockManifest == nil || m1.BlockManifest.Aggregate != m1.LogicalDigest {
		t.Fatalf("block manifest aggregate must equal the logical digest")
	}
	if !m1.PreEncryption {
		t.Fatal("a freshly computed manifest is over the pre-encryption bytes")
	}
	m2, _ := NewDiskManifestContext(context.Background(), DiskKindRAW, raw, MinDiskBlockSizeBytes)
	d1, _ := m1.Digest()
	d2, _ := m2.Digest()
	if d1 != d2 {
		t.Fatalf("disk manifest not deterministic: %s != %s", d1, d2)
	}

}

func TestDiskManifestBlockBoundsFailBeforeScanningContent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "must-not-be-opened.raw")
	for _, blockSize := range []int{MinDiskBlockSizeBytes - 1, MaxDiskBlockSizeBytes + 1} {
		if _, err := NewDiskManifestContext(context.Background(), DiskKindRAW, missing, blockSize); code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("NewDiskManifest block size %d code = %q, want %q (%v)",
				blockSize, code(err), nkderr.CodeInvalidCatalog, err)
		}
	}

	// A sparse fixture proves the entry bound is derived from metadata before the
	// implementation scans or retains one digest per logical block.
	oversized := filepath.Join(t.TempDir(), "too-many-blocks.raw")
	if err := os.WriteFile(oversized, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	size := int64(MaxDiskManifestBlocks+1) * int64(MinDiskBlockSizeBytes)
	if err := os.Truncate(oversized, size); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDiskManifestContext(context.Background(), DiskKindRAW, oversized, MinDiskBlockSizeBytes); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("oversized DiskManifest code = %q, want %q (%v)",
			code(err), nkderr.CodeInvalidCatalog, err)
	}
}

func TestDiskManifestContextFailsBeforeInputIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	missing := filepath.Join(t.TempDir(), "must-not-be-opened.raw")
	if _, err := NewDiskManifestContext(ctx, DiskKindRAW, missing,
		MinDiskBlockSizeBytes); code(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("canceled manifest construction returned %v", err)
	}
}

func TestDiskManifestValidationEnforcesResourceBounds(t *testing.T) {
	base := DiskManifest{
		SchemaVersion: DiskManifestSchemaVersion,
		DiskKind:      DiskKindRAW,
		LogicalDigest: "sha256:" + strings.Repeat("a", 64),
		PreEncryption: true,
		BlockManifest: &BlockManifest{
			BlockSizeBytes: MaxDiskBlockSizeBytes + 1,
			Aggregate:      "sha256:" + strings.Repeat("a", 64),
		},
		VirtualSizeBytes: 1,
	}
	if err := base.Validate(); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("oversized serialized block size code = %q, want %q (%v)",
			code(err), nkderr.CodeUnverifiedRef, err)
	}
	base.BlockManifest.BlockSizeBytes = MinDiskBlockSizeBytes
	base.VirtualSizeBytes = int64(MaxDiskManifestBlocks+1) * int64(MinDiskBlockSizeBytes)
	if err := base.Validate(); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("oversized serialized block count code = %q, want %q (%v)",
			code(err), nkderr.CodeUnverifiedRef, err)
	}
}

// ADR-14 §3: a LUKS/FDE-encrypted image fails closed — ciphertext is never the
// canonical pre-encryption digest.
func TestDiskManifestFailsClosedOnEncrypted(t *testing.T) {
	dir := t.TempDir()
	enc := filepath.Join(dir, "node.luks")
	// LUKS2 magic header.
	if err := os.WriteFile(enc, append([]byte{'L', 'U', 'K', 'S', 0xba, 0xbe}, make([]byte, 32)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDiskManifestContext(context.Background(), DiskKindRAW, enc, MinDiskBlockSizeBytes); code(err) != nkderr.CodeEncryptedArtifact {
		t.Fatalf("encrypted image must fail closed, got %v", err)
	}
}

func TestDiskManifestRejectsSemanticMutation(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "node.raw")
	if err := os.WriteFile(raw, []byte("this-is-a-fake-raw-node-disk-stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := NewDiskManifestContext(context.Background(), DiskKindRAW, raw, MinDiskBlockSizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	mutated := *manifest
	mutated.BlockManifest = &BlockManifest{
		BlockSizeBytes: manifest.BlockManifest.BlockSizeBytes,
		Aggregate:      manifest.BlockManifest.Aggregate,
		Blocks:         append([]string(nil), manifest.BlockManifest.Blocks...),
	}
	mutated.BlockManifest.Blocks[0] = "sha256:" + strings.Repeat("A", 64)
	if _, err := mutated.Digest(); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("malformed block identity must fail closed, got %v", err)
	}
	mutated = *manifest
	mutated.PreEncryption = false
	if _, err := mutated.Marshal(); code(err) != nkderr.CodeEncryptedArtifact {
		t.Fatalf("post-encryption manifest must fail closed, got %v", err)
	}
	mutated = *manifest
	mutated.MaskedRegions = []MaskedRegion{{OffsetBytes: 0, LengthBytes: 1, Reason: "ignored"}}
	if _, err := mutated.Digest(); code(err) != nkderr.CodeUnsupported {
		t.Fatalf("unimplemented masking must fail closed, got %v", err)
	}
}

func TestDiskManifestRejectsSymlinkInputs(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "node.raw")
	if err := os.WriteFile(raw, []byte("hello world!"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawLink := filepath.Join(dir, "node-link.raw")
	if err := os.Symlink(raw, rawLink); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDiskManifestContext(context.Background(), DiskKindRAW, rawLink, MinDiskBlockSizeBytes); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("symlinked canonical disk code = %q, want %q (%v)",
			code(err), nkderr.CodeVerifyFailed, err)
	}
	if runtime.GOOS != "linux" {
		return
	}

	manifest, err := NewDiskManifestContext(context.Background(), DiskKindRAW, raw, MinDiskBlockSizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	qemu, qemuDigest := fakeQemuImg(t, `{"format":"qcow2","virtual-size":12,"encrypted":false}`)
	codec, err := OpenQemuImgCodec(context.Background(), qemu, qemuDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	if err := manifest.AddEnvelope(context.Background(), codec, "qcow2", rawLink); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("symlinked envelope code = %q, want %q (%v)",
			code(err), nkderr.CodeVerifyFailed, err)
	}
}

func TestCanonicalDiskCapabilityDetectsMutationBetweenDerivations(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "node.raw")
	if err := os.WriteFile(raw, []byte("hello world!"), 0o600); err != nil {
		t.Fatal(err)
	}
	capability, err := openRegularFileCapabilityContext(t.Context(), raw, "canonical disk")
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	if _, _, _, err := capability.diskEvidenceContext(t.Context(), MinDiskBlockSizeBytes, true); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(raw, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("mutation")); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := capability.diskEvidenceContext(t.Context(), MinDiskBlockSizeBytes, true); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("mutated held canonical-disk capability code = %q, want %q (%v)",
			code(err), nkderr.CodeVerifyFailed, err)
	}
}

func TestAddEnvelopeUsesOneSnapshotWhenCallerPathMutates(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("qualified descriptor execution is Linux-only")
	}
	dir := t.TempDir()
	initial := []byte("hello world!")
	raw := filepath.Join(dir, "node.raw")
	envelope := filepath.Join(dir, "node.qcow2")
	if err := os.WriteFile(raw, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envelope, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := NewDiskManifestContext(context.Background(), DiskKindRAW, raw, MinDiskBlockSizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	wantContainerDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(initial))
	qemu, qemuDigest := fakeQemuImg(t, `{"format":"qcow2","virtual-size":12,"encrypted":false}`)
	codec, err := OpenQemuImgCodec(context.Background(), qemu, qemuDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	attackMarker := filepath.Join(t.TempDir(), "envelope-path-substituted")
	t.Setenv("NKD_RENAME_RESTORE_ENVELOPE", envelope)
	t.Setenv("NKD_ENVELOPE_ATTACK_MARKER", attackMarker)
	if err := manifest.AddEnvelope(context.Background(), codec, "qcow2", envelope); err != nil {
		t.Fatalf("caller-path mutation escaped the held snapshot: %v", err)
	}
	if len(manifest.Envelopes) != 1 || manifest.Envelopes[0].ContainerDigest != wantContainerDigest {
		t.Fatalf("envelope provenance used post-snapshot bytes: %+v", manifest.Envelopes)
	}
	restored, err := os.ReadFile(envelope)
	if err != nil || string(restored) != string(initial) {
		t.Fatalf("adversarial fixture did not restore caller path: %q (%v)", restored, err)
	}
	if _, err := os.Stat(attackMarker); err != nil {
		t.Fatalf("adversarial fixture did not substitute caller path: %v", err)
	}
}
