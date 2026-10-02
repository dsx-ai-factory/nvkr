package assemble

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// This opt-in replay uses the actual caller-supplied QEMU binary, not a fake
// probe. Normal contract tests remain independent of an installed QEMU closure.
func TestQualifiedVMDKRealQEMU(t *testing.T) {
	qemu := os.Getenv("NKD_TEST_QEMU_IMG")
	if qemu == "" {
		t.Skip("set NKD_TEST_QEMU_IMG to replay against a real QEMU runtime")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("real held QEMU replay requires Linux")
	}
	executable, err := os.ReadFile(qemu)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(executable))
	t.Logf("actual qemu-img executable=%s", digest)
	codec, err := OpenQemuImgCodec(t.Context(), qemu, os.Getenv("NKD_TEST_QEMU_IMG_DIGEST"))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	raw := filepath.Join(root, "input.raw")
	content := make([]byte, 1<<20)
	copy(content[4096:], "retained known logical RAW content")
	if err := os.WriteFile(raw, content, 0o400); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	for _, format := range []string{"monolithicSparse", "streamOptimized"} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(root, format+".vmdk")
			command := exec.CommandContext(ctx, qemu, "convert", "-f", "raw", "-O", "vmdk", "-o", "subformat="+format, raw, path)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("creating real QEMU fixture: %v: %s", err, output)
			}
			got, size, err := codec.EnvelopeLogicalDigest(ctx, "vmdk", path)
			if err != nil || got != want || size != int64(len(content)) {
				t.Fatalf("real %s decode=%s/%d err=%v, want %s/%d", format, got, size, err, want, len(content))
			}
			t.Logf("held %s reproduced RAW=%s bytes=%d", format, got, size)
		})
	}
	// Both supported external-extent entry routes are denied. The extent really
	// exists, so this cannot pass merely because an ambient pathname was missing.
	descriptor := []byte(fmt.Sprintf("# Disk DescriptorFile\nversion=1\nCID=fffffffe\nparentCID=ffffffff\ncreateType=\"monolithicFlat\"\nRW %d FLAT %q 0\n", len(content)/512, raw))
	redirect := vmdkSparseHeader()
	binary.LittleEndian.PutUint64(redirect[12:20], 0)
	binary.LittleEndian.PutUint64(redirect[28:36], 1)
	redirect = append(redirect, descriptor...)
	for _, tc := range []struct {
		name string
		data []byte
	}{{"text descriptor", descriptor}, {"binary descriptor redirect", redirect}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name+".vmdk")
			if err := os.WriteFile(path, tc.data, 0o400); err != nil {
				t.Fatal(err)
			}
			// Ungated QEMU must actually admit this external layout. Otherwise
			// rejection could merely exercise a malformed fixture, not containment.
			// Only this test's known private RAW file is available to the descriptor.
			command := exec.CommandContext(ctx, qemu, "info", "-f", "vmdk", "--output=json", path)
			output, err := command.Output()
			if err != nil {
				t.Fatalf("external-extent fixture is not accepted by real QEMU: %v", err)
			}
			var info qemuInfo
			if err := json.Unmarshal(output, &info); err != nil {
				t.Fatal(err)
			}
			extents := info.FormatSpecific.Data.Extents
			if len(extents) != 1 || extents[0].Filename != raw || extents[0].VirtualSize != int64(len(content)) {
				t.Fatalf("real QEMU did not open the known external RAW extent: %s", output)
			}
			if _, _, err := codec.EnvelopeLogicalDigest(ctx, "vmdk", path); code(err) != e.CodeEnvelopeDecode {
				t.Fatalf("real external-extent fixture returned %v", err)
			}
		})
	}
}
