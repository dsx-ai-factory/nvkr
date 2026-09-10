package assemble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// This file implements ADR-14's single qualified disk-codec seam. The executable
// is captured once as an anonymous immutable capability and checked against an
// exact local pin before it can inspect or decode an envelope.

const QemuImgDefault = "qemu-img"

const (
	maxQemuImgExecutableBytes int64 = 512 << 20
	maxHeldDiskImageBytes     int64 = 1 << 50 // v1 qualification supports logical disks up to 1 PiB
)

// QemuImgCodec is an opaque, process-local capability for one exact qemu-img
// executable. The capability and validity marker are intentionally private.
type QemuImgCodec struct {
	mu         sync.Mutex
	executable *heldfile.File
	digest     string
	closed     bool
}

// OpenQemuImgCodec snapshots qemuImg and verifies its exact lowercase SHA-256
// identity before returning a decoder capability. The original path is never used
// after this function returns.
func OpenQemuImgCodec(ctx context.Context, qemuImg, expectedDigest string) (*QemuImgCodec, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "opening a disk codec requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening pinned qemu-img codec")
	}
	if !canonical.IsDigest(expectedDigest) {
		return nil, e.New(e.CodeUnpinnedInput,
			"qemu-img requires an exact lowercase sha256 executable digest, got %q", expectedDigest)
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err,
			"holding qemu-img executable by descriptor")
	}
	resolved, err := exec.LookPath(qemuImg)
	if err != nil {
		return nil, e.Wrap(e.CodeEnvelopeDecode, err, "finding pinned qemu-img %q", qemuImg)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return nil, e.Wrap(e.CodeEnvelopeDecode, err, "resolving qemu-img path %s", resolved)
	}
	executable, err := heldfile.OpenContext(ctx, resolved, heldfile.Options{
		Label: "qemu-img", Executable: true,
		MaxBytes: maxQemuImgExecutableBytes, ExpectedDigest: expectedDigest,
	})
	if err != nil {
		if errors.Is(err, heldfile.ErrUnsupported) {
			return nil, e.Wrap(e.CodeEngineCapability, err,
				"holding qemu-img executable %s by descriptor", resolved)
		}
		return nil, e.Wrap(e.CodeUnverifiedRef, err,
			"capturing pinned qemu-img executable %s", resolved)
	}
	if err := ctx.Err(); err != nil {
		_ = executable.Close()
		return nil, e.Wrap(e.CodeTimeout, err, "opening pinned qemu-img codec")
	}
	return &QemuImgCodec{executable: executable, digest: executable.Digest()}, nil
}

// Digest returns the exact decoder executable identity bound by this capability.
func (codec *QemuImgCodec) Digest() (string, error) {
	return codec.DigestContext(context.Background())
}

// DigestContext returns the exact decoder executable identity after revalidating
// the held capability with cancellation.
func (codec *QemuImgCodec) DigestContext(ctx context.Context) (string, error) {
	if codec == nil {
		return "", e.New(e.CodeTrustPolicyMissing, "qualified qemu-img codec is required")
	}
	if err := codec.lockContext(ctx, "verifying held qemu-img codec"); err != nil {
		return "", err
	}
	defer codec.mu.Unlock()
	if err := codec.requireSnapshotContext(ctx); err != nil {
		return "", err
	}
	return codec.digest, nil
}

// Close releases the held executable capability. It is idempotent.
func (codec *QemuImgCodec) Close() error {
	if codec == nil {
		return nil
	}
	codec.mu.Lock()
	defer codec.mu.Unlock()
	if codec.closed {
		return nil
	}
	codec.closed = true
	err := codec.executable.Close()
	codec.executable = nil
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "closing held qemu-img executable")
	}
	return nil
}

// EnvelopeLogicalDigest decodes a clean derived envelope to a private RAW file and
// hashes its complete logical stream. Encrypted, backing-file, and format-confused
// envelopes fail before conversion.
func (codec *QemuImgCodec) EnvelopeLogicalDigest(ctx context.Context, format, envelopePath string) (string, int64, error) {
	if codec == nil {
		return "", 0, e.New(e.CodeTrustPolicyMissing, "qualified qemu-img codec is required")
	}
	if ctx == nil {
		return "", 0, e.New(e.CodeUsage, "decoding a disk envelope requires a non-nil context")
	}
	if !validEnvelopeFormat(format) {
		return "", 0, e.New(e.CodeEnvelopeDecode, "unknown envelope format %q (qcow2|vmdk|vhd|vpc)", format)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, e.Wrap(e.CodeTimeout, err, "decoding disk envelope")
	}
	input, err := openHeldEnvelope(ctx, envelopePath)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = input.Close() }()
	return codec.envelopeLogicalDigest(ctx, format, input, envelopePath)
}

func (codec *QemuImgCodec) envelopeLogicalDigest(ctx context.Context, format string, input *heldfile.File, displayPath string) (string, int64, error) {
	if codec == nil {
		return "", 0, e.New(e.CodeTrustPolicyMissing, "qualified qemu-img codec is required")
	}
	if ctx == nil {
		return "", 0, e.New(e.CodeUsage, "decoding a disk envelope requires a non-nil context")
	}
	if !validEnvelopeFormat(format) {
		return "", 0, e.New(e.CodeEnvelopeDecode, "unknown envelope format %q (qcow2|vmdk|vhd|vpc)", format)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, e.Wrap(e.CodeTimeout, err, "decoding disk envelope")
	}
	if err := codec.lockContext(ctx, "waiting for the qemu-img codec"); err != nil {
		return "", 0, err
	}
	defer codec.mu.Unlock()
	if err := codec.requireSnapshotContext(ctx); err != nil {
		return "", 0, err
	}
	if err := assertHeldPreEncryption(ctx, input, "disk envelope "+displayPath); err != nil {
		return "", 0, err
	}
	virtualSize, err := codec.probe(ctx, format, input)
	if err != nil {
		return "", 0, err
	}
	decoded, err := heldfile.NewOutput("decoded-raw", maxHeldDiskImageBytes)
	if err != nil {
		return "", 0, e.Wrap(e.CodeEngineCapability, err, "creating held decoded RAW output")
	}
	defer func() { _ = decoded.Close() }()
	inputPath, err := heldfile.ChildPath(1)
	if err != nil {
		return "", 0, e.Wrap(e.CodeEngineCapability, err, "mapping held disk envelope")
	}
	outputPath, err := heldfile.ChildPath(2)
	if err != nil {
		return "", 0, e.Wrap(e.CodeEngineCapability, err, "mapping held decoded RAW output")
	}
	var commandOutput bytes.Buffer
	err = heldfile.RunContext(ctx, codec.executable,
		[]string{"convert", "-f", qemuFormat(format), "-O", "raw", inputPath, outputPath},
		heldfile.RunOptions{Stdout: &commandOutput, Stderr: &commandOutput}, input, decoded)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, e.Wrap(e.CodeTimeout, ctx.Err(), "decoding %s envelope", format)
		}
		if errors.Is(err, heldfile.ErrUnsupported) {
			return "", 0, e.Wrap(e.CodeEngineCapability, err,
				"executing held qemu-img decoder")
		}
		return "", 0, e.Wrap(e.CodeEnvelopeDecode, err,
			"qemu-img convert %s to raw failed: %s", displayPath, commandOutput.String())
	}
	if err := codec.requireSnapshotContext(ctx); err != nil {
		return "", 0, err
	}
	sealed, err := decoded.SealContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, e.Wrap(e.CodeTimeout, ctx.Err(), "sealing decoded RAW output")
		}
		return "", 0, e.Wrap(e.CodeVerifyFailed, err, "sealing decoded RAW output")
	}
	defer func() { _ = sealed.Close() }()
	if err := assertHeldPreEncryption(ctx, sealed, "decoded RAW"); err != nil {
		return "", 0, err
	}
	if err := sealed.AssertUnchangedContext(ctx); err != nil {
		if ctx.Err() != nil {
			return "", 0, e.Wrap(e.CodeTimeout, ctx.Err(), "verifying decoded RAW output")
		}
		return "", 0, e.Wrap(e.CodeVerifyFailed, err, "verifying decoded RAW output")
	}
	if sealed.Size() != virtualSize {
		return "", 0, e.New(e.CodeEnvelopeDecode,
			"decoded RAW size %d does not match probed virtual size %d", sealed.Size(), virtualSize)
	}
	return sealed.Digest(), sealed.Size(), nil
}

type qemuInfo struct {
	Format              string `json:"format"`
	VirtualSize         int64  `json:"virtual-size"`
	Encrypted           bool   `json:"encrypted"`
	BackingFilename     string `json:"backing-filename"`
	FullBackingFilename string `json:"full-backing-filename"`
}

func (codec *QemuImgCodec) probe(ctx context.Context, format string, input *heldfile.File) (int64, error) {
	inputPath, err := heldfile.ChildPath(1)
	if err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "mapping held disk envelope for qemu-img info")
	}
	var stdout, stderr bytes.Buffer
	err = heldfile.RunContext(ctx, codec.executable,
		[]string{"info", "--output=json", inputPath},
		heldfile.RunOptions{Stdout: &stdout, Stderr: &stderr}, input)
	if err != nil {
		if ctx.Err() != nil {
			return 0, e.Wrap(e.CodeTimeout, ctx.Err(), "probing %s envelope", format)
		}
		if errors.Is(err, heldfile.ErrUnsupported) {
			return 0, e.Wrap(e.CodeEngineCapability, err,
				"executing held qemu-img probe")
		}
		return 0, e.Wrap(e.CodeEnvelopeDecode, err,
			"qemu-img info failed for held %s envelope: %s", format, stderr.String())
	}
	var info qemuInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return 0, e.Wrap(e.CodeEnvelopeDecode, err, "decoding qemu-img info for held %s envelope", format)
	}
	if info.Format != qemuFormat(format) || info.VirtualSize <= 0 || info.VirtualSize > maxHeldDiskImageBytes {
		return 0, e.New(e.CodeEnvelopeDecode,
			"envelope probe reported format %q and virtual size %d, want %q and size in [1,%d]",
			info.Format, info.VirtualSize, qemuFormat(format), maxHeldDiskImageBytes)
	}
	if info.Encrypted {
		return 0, e.New(e.CodeEncryptedArtifact,
			"held input is an encrypted %s envelope; reproduction identity attaches before encryption", format)
	}
	if info.BackingFilename != "" || info.FullBackingFilename != "" {
		return 0, e.New(e.CodeEnvelopeDecode,
			"held %s envelope depends on a backing file and is not self-contained", format)
	}
	return info.VirtualSize, nil
}

func (codec *QemuImgCodec) requireSnapshotContext(ctx context.Context) error {
	if codec.closed || codec.executable == nil {
		return e.New(e.CodeTrustPolicyMissing, "qualified qemu-img codec is closed")
	}
	if err := codec.executable.AssertUnchangedContext(ctx); err != nil {
		if ctx != nil && ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "verifying held qemu-img executable")
		}
		return e.Wrap(e.CodeUnverifiedRef, err, "verifying held qemu-img executable")
	}
	return nil
}

func (codec *QemuImgCodec) lockContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", operation)
	}
	for {
		if codec.mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return e.Wrap(e.CodeTimeout, ctx.Err(), "%s", operation)
		case <-timer.C:
		}
	}
}

func openHeldEnvelope(ctx context.Context, path string) (*heldfile.File, error) {
	input, err := heldfile.OpenContext(ctx, path, heldfile.Options{
		Label: "disk-envelope", MaxBytes: maxHeldDiskImageBytes,
	})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "capturing disk envelope %s", path)
		}
		if errors.Is(err, heldfile.ErrUnsupported) {
			return nil, e.Wrap(e.CodeEngineCapability, err, "holding disk envelope by descriptor")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "capturing disk envelope %s", path)
	}
	return input, nil
}

func assertHeldPreEncryption(ctx context.Context, input *heldfile.File, label string) error {
	if input == nil {
		return e.New(e.CodeVerifyFailed, "%s capability is missing", label)
	}
	if ctx == nil {
		return e.New(e.CodeUsage, "verifying %s requires a non-nil context", label)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "verifying %s", label)
	}
	header := make([]byte, len(luksMagic))
	n, err := input.ReadAt(header, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return e.Wrap(e.CodeVerifyFailed, err, "reading encryption header from %s", label)
	}
	if n == len(luksMagic) && bytes.Equal(header, luksMagic) {
		return e.New(e.CodeEncryptedArtifact,
			"%s is LUKS/FDE-encrypted: byte identity attaches to the pre-encryption RAW", label)
	}
	if err := input.AssertUnchangedContext(ctx); err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "verifying %s", label)
		}
		return e.Wrap(e.CodeVerifyFailed, err, "verifying %s", label)
	}
	return nil
}

func qemuFormat(format string) string {
	if format == "vhd" {
		return "vpc"
	}
	return format
}

func validEnvelopeFormat(format string) bool {
	switch format {
	case "qcow2", "vmdk", "vhd", "vpc":
		return true
	default:
		return false
	}
}

// luksMagic is the LUKS1/LUKS2 on-disk header magic.
var luksMagic = []byte{'L', 'U', 'K', 'S', 0xba, 0xbe}
