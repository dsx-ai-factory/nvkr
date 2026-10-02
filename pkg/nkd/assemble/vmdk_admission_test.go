package assemble

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func vmdkSparseHeader() []byte {
	header := make([]byte, 512)
	copy(header, "KDMV")
	binary.LittleEndian.PutUint32(header[4:8], 1)
	binary.LittleEndian.PutUint64(header[12:20], 1)
	return header
}

func TestQualifiedVMDKRejectsDescriptorBeforeProbe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held executable execution requires Linux")
	}
	redirect := vmdkSparseHeader()
	binary.LittleEndian.PutUint64(redirect[12:20], 0)
	binary.LittleEndian.PutUint64(redirect[28:36], 1)
	legacy := vmdkSparseHeader()
	copy(legacy, "COWD")
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"external descriptor", []byte("# Disk DescriptorFile\nversion=1\ncreateType=\"monolithicFlat\"\nRW 1 FLAT \"/etc/passwd\" 0\n")},
		{"binary descriptor redirect", redirect},
		{"legacy sparse", legacy},
		{"truncated sparse", vmdkSparseHeader()[:511]},
		{"empty", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "probe-called")
			t.Setenv("NKD_CODEC_PROBE_MARKER", marker)
			tool, digest := fakeQemuImgDriver(t, `{"format":"vmdk","virtual-size":512}`, "vmdk")
			codec, err := OpenQemuImgCodec(t.Context(), tool, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer codec.Close()
			path := filepath.Join(t.TempDir(), "subject")
			if err := os.WriteFile(path, tc.data, 0o400); err != nil {
				t.Fatal(err)
			}
			if _, _, err := codec.EnvelopeLogicalDigest(t.Context(), "vmdk", path); code(err) != e.CodeEnvelopeDecode {
				t.Fatalf("unadmitted layout returned %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("unadmitted VMDK started probe: %v", err)
			}
		})
	}
}

func TestQualifiedVMDKRequiresHeldSparseExtent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held executable execution requires Linux")
	}
	valid := `{"format":"vmdk","virtual-size":512,"format-specific":{"type":"vmdk","data":{"create-type":"monolithicSparse","extents":[{"filename":"/proc/self/fd/4","format":"SPARSE","virtual-size":512}]}}}`
	for _, tc := range []struct {
		name string
		info string
		code e.Code
	}{
		{"monolithic sparse", valid, ""},
		{"native empty extent label", strings.Replace(valid, `"format":"SPARSE"`, `"format":""`, 1), ""},
		{"missing extent label", strings.Replace(valid, `"format":"SPARSE",`, "", 1), e.CodeEnvelopeDecode},
		{"stream optimized", strings.Replace(valid, "monolithicSparse", "streamOptimized", 1), ""},
		{"missing extent metadata", `{"format":"vmdk","virtual-size":512}`, e.CodeEnvelopeDecode},
		{"wrong specific type", strings.Replace(valid, `"type":"vmdk"`, `"type":"qcow2"`, 1), e.CodeEnvelopeDecode},
		{"descriptor create type", strings.Replace(valid, "monolithicSparse", "monolithicFlat", 1), e.CodeEnvelopeDecode},
		{"external extent", strings.Replace(valid, "/proc/self/fd/4", "/etc/passwd", 1), e.CodeEnvelopeDecode},
		{"wrong extent format", strings.Replace(valid, "SPARSE", "FLAT", 1), e.CodeEnvelopeDecode},
		{"incomplete extent", strings.Replace(valid, `"virtual-size":512}]`, `"virtual-size":256}]`, 1), e.CodeEnvelopeDecode},
		{"multiple extents", strings.Replace(valid, `}]`, `},{"filename":"/proc/self/fd/4","format":"SPARSE","virtual-size":512}]`, 1), e.CodeEnvelopeDecode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "convert-called")
			t.Setenv("NKD_CODEC_CONVERT_MARKER", marker)
			tool, digest := fakeQemuImgDriver(t, tc.info, "vmdk")
			codec, err := OpenQemuImgCodec(t.Context(), tool, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer codec.Close()
			path := filepath.Join(t.TempDir(), "subject")
			if err := os.WriteFile(path, vmdkSparseHeader(), 0o400); err != nil {
				t.Fatal(err)
			}
			_, size, err := codec.EnvelopeLogicalDigest(t.Context(), "vmdk", path)
			if code(err) != tc.code {
				t.Fatalf("extent admission returned %v, want %s", err, tc.code)
			}
			if tc.code == "" {
				if size != 512 {
					t.Fatalf("decoded size=%d", size)
				}
				return
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("unadmitted VMDK started conversion: %v", err)
			}
		})
	}
}
