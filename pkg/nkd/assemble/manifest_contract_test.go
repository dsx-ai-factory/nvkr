package assemble

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDiskManifestRejectsInvalidSerializedDescriptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.raw")
	if err := os.WriteFile(path, []byte("canonical disk bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*DiskManifest)
		want   e.Code
	}{
		{"schema", func(m *DiskManifest) { m.SchemaVersion = "unknown" }, e.CodeInvalidCatalog},
		{"kind", func(m *DiskManifest) { m.DiskKind = "unknown" }, e.CodeUnsupported},
		{"role", func(m *DiskManifest) { m.Role = "unknown" }, e.CodeUnsupported},
		{"logical digest", func(m *DiskManifest) { m.LogicalDigest = "invalid" }, e.CodeUnverifiedRef},
		{"size", func(m *DiskManifest) { m.VirtualSizeBytes = 0 }, e.CodeUnverifiedRef},
		{"encrypted", func(m *DiskManifest) { m.PreEncryption = false }, e.CodeEncryptedArtifact},
		{"masked region", func(m *DiskManifest) { m.MaskedRegions = []MaskedRegion{{LengthBytes: 1, Reason: "ignored"}} }, e.CodeUnsupported},
		{"ISO with RAW blocks", func(m *DiskManifest) { m.DiskKind = DiskKindISO }, e.CodeInvalidCatalog},
		{"missing blocks", func(m *DiskManifest) { m.BlockManifest = nil }, e.CodeUnverifiedRef},
		{"bad block", func(m *DiskManifest) { m.BlockManifest.Blocks[0] = "invalid" }, e.CodeUnverifiedRef},
		{"unsupported envelope", func(m *DiskManifest) { m.Envelopes = []EnvelopeRef{{Format: "vpc"}} }, e.CodeUnsupported},
		{"unpinned envelope", func(m *DiskManifest) { m.Envelopes = []EnvelopeRef{{Format: "qcow2"}} }, e.CodeUnverifiedRef},
		{"duplicate envelope", func(m *DiskManifest) {
			d := "sha256:" + strings.Repeat("a", 64)
			m.Envelopes = []EnvelopeRef{{Format: "qcow2", ContainerDigest: d, DecoderDigest: d}, {Format: "qcow2", ContainerDigest: d, DecoderDigest: d}}
		}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := NewDiskManifestContext(t.Context(), DiskKindRAW, "worker", path, MinDiskBlockSizeBytes)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(manifest)
			if err := manifest.Validate(); code(err) != tc.want {
				t.Fatalf("validation = %v, want %s", err, tc.want)
			}
			if raw, err := manifest.Marshal(); code(err) != tc.want || raw != nil {
				t.Fatalf("invalid manifest serialized: %s, %v", raw, err)
			}
		})
	}
}

func TestISOManifestSerializationPreservesFlatStreamIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.iso")
	if err := os.WriteFile(path, []byte("diagnostic ISO stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := NewDiskManifestContext(t.Context(), DiskKindISO, "worker", path, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded DiskManifest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	want, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	got, err := decoded.Digest()
	if err != nil || got != want || decoded.BlockManifest != nil || len(decoded.Envelopes) != 0 {
		t.Fatalf("ISO changed identity: %+v, %v", decoded, err)
	}
}
