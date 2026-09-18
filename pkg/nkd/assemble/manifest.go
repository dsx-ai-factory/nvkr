package assemble

import (
	"context"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// This file implements the ADR-14 §1 unified DiskManifest. The DiskManifest is the single canonical description of a bootable node
// disk: the logical digest over the decoded RAW stream, the per-block manifest for
// diagnosable divergence, envelope refs (container digest = PROVENANCE ONLY, never the
// trust anchor), the pre-encryption boundary. It serializes through
// the ADR-01 canonical codec and folds into the evidence/validation vector (ADR-05).

// DiskKind distinguishes the two canonical disk classes (ADR-14 §1/§4): a block image
// whose derived envelopes decode to the same RAW logical stream, and an ISO which is
// itself a flat logical stream (no decode-to-RAW).
const (
	DiskKindRAW = "raw" // canonical block image; qcow2/vmdk/vhd are derived envelopes
	DiskKindISO = "iso" // second canonical class: a flat logical stream, hashed directly
)

// EnvelopeRef records a derived envelope's format + its CONTAINER digest. The container
// digest is provenance only — verification decodes the envelope and compares the
// logical digest, never these bytes (ADR-14 §1).
type EnvelopeRef struct {
	Format          string `json:"format"` // qcow2 | vmdk | vhd
	ContainerDigest string `json:"containerDigest"`
	DecoderDigest   string `json:"decoderDigest"`
}

// BlockManifest records fixed-size block digests in canonical RAW LBA order.
// Aggregate identifies the complete logical stream, excluding envelope headers
// and allocation metadata; block digests locate divergence within that stream.
type BlockManifest struct {
	BlockSizeBytes int      `json:"blockSizeBytes"`
	Aggregate      string   `json:"aggregate"`
	Blocks         []string `json:"blocks"`
}

// DiskManifest is the unified canonical-disk manifest (ADR-14 §1). LogicalDigest is the
// trust anchor (over the decoded RAW / flat ISO logical stream). BlockManifest gives
// diagnosable per-block granularity. Envelopes are provenance references. PreEncryption
// marks that LogicalDigest is over the pre-encryption bytes; an encrypted output forks
// its own non-reproduction identity (§3).
type DiskManifest struct {
	SchemaVersion    string         `json:"schemaVersion"`
	DiskKind         string         `json:"diskKind"` // raw | iso
	LogicalDigest    string         `json:"logicalDigest"`
	VirtualSizeBytes int64          `json:"virtualSizeBytes"`
	BlockManifest    *BlockManifest `json:"blockManifest,omitempty"`
	Envelopes        []EnvelopeRef  `json:"envelopes,omitempty"`
	PreEncryption    bool           `json:"preEncryption"`
	MaskedRegions    []MaskedRegion `json:"maskedRegions,omitempty"`
}

// MaskedRegion is a bounded, DECLARED region excluded from byte-identity (ADR-14 §2's
// ext4-residual fallback): a mismatch inside it is not a reproduction failure. Declaring
// it in the manifest keeps the exclusion auditable rather than silent.
type MaskedRegion struct {
	OffsetBytes int64  `json:"offsetBytes"`
	LengthBytes int64  `json:"lengthBytes"`
	Reason      string `json:"reason"`
}

const DiskManifestSchemaVersion = "nkd.dev/disk-manifest/v2"

// Disk block manifests are intentionally bounded in both dimensions. The block-size
// ceiling bounds the one-chunk working allocation, while the entry ceiling bounds
// the retained digest slice and its canonical serialization. Callers with larger
// disks must select a coarser block size; silently allocating an attacker-sized
// manifest is never an acceptable verification strategy.
const (
	MinDiskBlockSizeBytes = 4 << 10
	MaxDiskBlockSizeBytes = 16 << 20
	MaxDiskManifestBlocks = 100_000
)

// NewDiskManifestContext builds the release-qualified canonical disk manifest.
func NewDiskManifestContext(ctx context.Context, diskKind, rawPath string, blockSize int) (*DiskManifest, error) {
	if err := checkContext(ctx, "building a canonical disk manifest"); err != nil {
		return nil, err
	}
	if diskKind != DiskKindRAW && diskKind != DiskKindISO {
		return nil, e.New(e.CodeUnsupported, "unknown disk kind %q (raw|iso)", diskKind)
	}
	if diskKind == DiskKindRAW {
		// Validate the caller-controlled allocation dimension before opening rawPath.
		if _, err := boundedDiskBlockCount(0, blockSize, e.CodeInvalidCatalog); err != nil {
			return nil, err
		}
	}
	snapshot, err := openRegularFileCapabilityContext(ctx, rawPath, "canonical disk "+rawPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = snapshot.Close() }()
	if diskKind == DiskKindRAW {
		// The held descriptor's initial size makes the retained manifest allocation
		// predictable before any content is scanned.
		if _, err := boundedDiskBlockCount(snapshot.info.Size(), blockSize, e.CodeInvalidCatalog); err != nil {
			return nil, err
		}
	}
	dig, size, blockManifest, err := snapshot.diskEvidenceContext(ctx, blockSize, diskKind == DiskKindRAW)
	if err != nil {
		return nil, err
	}
	m := &DiskManifest{
		SchemaVersion: DiskManifestSchemaVersion, DiskKind: diskKind,
		LogicalDigest: dig, VirtualSizeBytes: size, PreEncryption: true,
	}
	if diskKind == DiskKindRAW {
		m.BlockManifest = blockManifest
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := checkContext(ctx, "building a canonical disk manifest"); err != nil {
		return nil, err
	}
	return m, nil
}

// AddEnvelope decodes a derived envelope through an exact pinned codec, asserts its
// decoded logical stream matches this manifest, and records independently computed
// container and decoder digests as provenance.
func (m *DiskManifest) AddEnvelope(ctx context.Context, codec *QemuImgCodec, format, envelopePath string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.DiskKind != DiskKindRAW {
		return e.New(e.CodeUnsupported, "ISO is a canonical flat stream and cannot carry a decoded envelope")
	}
	if codec == nil {
		return e.New(e.CodeTrustPolicyMissing, "qualified qemu-img codec is required")
	}
	if ctx == nil {
		return e.New(e.CodeUsage, "adding a disk envelope requires a non-nil context")
	}
	if !validEnvelopeFormat(format) {
		return e.New(e.CodeEnvelopeDecode, "unknown envelope format %q (qcow2|vmdk|vhd|vpc)", format)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "adding disk envelope")
	}
	snapshot, err := openHeldEnvelope(ctx, envelopePath)
	if err != nil {
		return err
	}
	defer func() { _ = snapshot.Close() }()
	if err := assertHeldPreEncryption(ctx, snapshot, "disk envelope "+envelopePath); err != nil {
		return err
	}
	containerDigest, containerSize := snapshot.Digest(), snapshot.Size()
	got, size, err := codec.envelopeLogicalDigest(ctx, format, snapshot, envelopePath)
	if err != nil {
		return err
	}
	if got != m.LogicalDigest || size != m.VirtualSizeBytes {
		return e.New(e.CodeEnvelopeDecode,
			"envelope %s decodes to %s/%d bytes, not canonical logical identity %s/%d bytes",
			envelopePath, got, size, m.LogicalDigest, m.VirtualSizeBytes)
	}
	if err := snapshot.AssertUnchangedContext(ctx); err != nil ||
		snapshot.Digest() != containerDigest || snapshot.Size() != containerSize {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "verifying private disk envelope snapshot")
		}
		return e.New(e.CodeVerifyFailed, "private disk envelope snapshot changed while decoding")
	}
	decoderDigest, err := codec.DigestContext(ctx)
	if err != nil {
		return err
	}
	candidate := append([]EnvelopeRef(nil), m.Envelopes...)
	candidate = append(candidate, EnvelopeRef{
		Format: format, ContainerDigest: containerDigest, DecoderDigest: decoderDigest,
	})
	sort.Slice(candidate, func(i, j int) bool { return candidate[i].Format < candidate[j].Format })
	copy := *m
	copy.Envelopes = candidate
	if err := copy.Validate(); err != nil {
		return err
	}
	m.Envelopes = candidate
	return nil
}

// Validate enforces the supported disk-manifest profile before it can acquire an
// identity. Masked regions remain a reserved schema hook until a verifier implements
// the corresponding masked comparison algorithm.
func (m *DiskManifest) Validate() error {
	if m == nil {
		return e.New(e.CodeInvalidCatalog, "DiskManifest is required")
	}
	if m.SchemaVersion != DiskManifestSchemaVersion {
		return e.New(e.CodeInvalidCatalog, "DiskManifest schemaVersion %q is unsupported", m.SchemaVersion)
	}
	if m.DiskKind != DiskKindRAW && m.DiskKind != DiskKindISO {
		return e.New(e.CodeUnsupported, "unknown disk kind %q (raw|iso)", m.DiskKind)
	}
	if !canonical.IsDigest(m.LogicalDigest) || m.VirtualSizeBytes <= 0 {
		return e.New(e.CodeUnverifiedRef, "DiskManifest must bind an exact logical digest and positive virtual size")
	}
	if !m.PreEncryption {
		return e.New(e.CodeEncryptedArtifact, "DiskManifest is not at the pre-encryption reproduction boundary")
	}
	if len(m.MaskedRegions) != 0 {
		return e.New(e.CodeUnsupported,
			"masked disk regions are reserved until a bounded masked-comparison verifier is configured")
	}
	if m.DiskKind == DiskKindISO {
		if m.BlockManifest != nil || len(m.Envelopes) != 0 {
			return e.New(e.CodeInvalidCatalog, "ISO is a canonical flat stream and cannot carry RAW blocks or envelopes")
		}
		return nil
	}
	if m.BlockManifest == nil || m.BlockManifest.Aggregate != m.LogicalDigest {
		return e.New(e.CodeUnverifiedRef, "RAW DiskManifest has no exact matching block manifest")
	}
	wantBlocks, err := boundedDiskBlockCount(
		m.VirtualSizeBytes, m.BlockManifest.BlockSizeBytes, e.CodeUnverifiedRef,
	)
	if err != nil {
		return err
	}
	if len(m.BlockManifest.Blocks) != wantBlocks {
		return e.New(e.CodeUnverifiedRef,
			"RAW DiskManifest has %d block identities, want %d for its virtual size", len(m.BlockManifest.Blocks), wantBlocks)
	}
	for i, digest := range m.BlockManifest.Blocks {
		if !canonical.IsDigest(digest) {
			return e.New(e.CodeUnverifiedRef, "RAW DiskManifest block %d has a non-canonical digest", i)
		}
	}
	previousFormat := ""
	for i, envelope := range m.Envelopes {
		if !validEnvelopeFormat(envelope.Format) || envelope.Format == "vpc" {
			return e.New(e.CodeUnsupported, "DiskManifest envelope %d has unsupported public format %q", i, envelope.Format)
		}
		if i > 0 && envelope.Format <= previousFormat {
			return e.New(e.CodeInvalidCatalog, "DiskManifest envelopes must be strictly sorted and unique by format")
		}
		previousFormat = envelope.Format
		if !canonical.IsDigest(envelope.ContainerDigest) || !canonical.IsDigest(envelope.DecoderDigest) {
			return e.New(e.CodeUnverifiedRef,
				"DiskManifest envelope %q lacks exact container or decoder identity", envelope.Format)
		}
	}
	return nil
}

// boundedDiskBlockCount validates the two memory-bearing block-manifest dimensions
// and returns the exact number of retained digests without overflow. code lets
// construction reject an invalid caller request while validation classifies an
// already-materialized untrusted manifest as an unverified reference.
func boundedDiskBlockCount(size int64, blockSize int, code e.Code) (int, error) {
	if blockSize < MinDiskBlockSizeBytes || blockSize > MaxDiskBlockSizeBytes {
		return 0, e.New(code,
			"disk block size %d is outside the supported range [%d,%d] bytes",
			blockSize, MinDiskBlockSizeBytes, MaxDiskBlockSizeBytes)
	}
	if size < 0 {
		return 0, e.New(code, "disk size %d is invalid", size)
	}
	blocks := int64(0)
	if size > 0 {
		blocks = 1 + (size-1)/int64(blockSize)
	}
	if blocks > MaxDiskManifestBlocks {
		return 0, e.New(code,
			"disk requires %d block identities at %d bytes per block; maximum is %d",
			blocks, blockSize, MaxDiskManifestBlocks)
	}
	return int(blocks), nil
}

// Digest is the disk manifest's validated canonical content digest (ADR-01 codec).
func (m *DiskManifest) Digest() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindDiskManifest, m)
}

// Marshal renders the disk manifest in the canonical JCS codec.
func (m *DiskManifest) Marshal() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(m)
}
