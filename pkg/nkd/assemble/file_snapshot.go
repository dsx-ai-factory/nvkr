package assemble

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

// regularFileCapability holds one O_NOFOLLOW regular-file descriptor. Callers
// derive every identity facet from this capability instead of reopening a
// caller-controlled pathname between encryption, aggregate, and block checks.
type regularFileCapability struct {
	path  string
	file  *os.File
	info  os.FileInfo
	label string
}

func openRegularFileCapabilityContext(ctx context.Context, path, label string) (*regularFileCapability, error) {
	if err := checkContext(ctx, "opening "+label); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening %s without following links", label)
	}
	source := os.NewFile(uintptr(fd), path)
	if source == nil {
		_ = unix.Close(fd)
		return nil, e.New(e.CodeInternal, "opening %s file capability", label)
	}
	owned := true
	defer func() {
		if owned {
			_ = source.Close()
		}
	}()
	before, err := source.Stat()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "inspecting %s", label)
	}
	if !before.Mode().IsRegular() {
		return nil, e.New(e.CodeVerifyFailed, "%s must be a regular file", label)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(before, pathInfo) {
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "%s changed while opening", label)
		}
		return nil, e.New(e.CodeVerifyFailed, "%s changed while opening", label)
	}
	if err := checkContext(ctx, "opening "+label); err != nil {
		return nil, err
	}
	owned = false
	return &regularFileCapability{path: path, file: source, info: before, label: label}, nil
}

func (snapshot *regularFileCapability) Close() error {
	if snapshot == nil {
		return nil
	}
	var closeErr error
	if snapshot.file != nil {
		closeErr = snapshot.file.Close()
		snapshot.file = nil
	}
	if closeErr != nil {
		return e.Wrap(e.CodeInternal, closeErr, "closing private %s snapshot", snapshot.label)
	}
	return nil
}

func (snapshot *regularFileCapability) rewindAndVerifyContext(ctx context.Context) error {
	if err := checkContext(ctx, "verifying a private regular-file snapshot"); err != nil {
		return err
	}
	if snapshot == nil || snapshot.file == nil {
		return e.New(e.CodeInternal, "private regular-file snapshot is closed")
	}
	current, err := snapshot.file.Stat()
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "inspecting private %s snapshot", snapshot.label)
	}
	pathInfo, pathErr := os.Lstat(snapshot.path)
	if pathErr != nil || !current.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
		!os.SameFile(snapshot.info, current) || !os.SameFile(snapshot.info, pathInfo) ||
		current.Size() != snapshot.info.Size() || !current.ModTime().Equal(snapshot.info.ModTime()) ||
		current.Mode() != snapshot.info.Mode() {
		if pathErr != nil {
			return e.Wrap(e.CodeVerifyFailed, pathErr, "private %s snapshot changed", snapshot.label)
		}
		return e.New(e.CodeVerifyFailed, "private %s snapshot changed", snapshot.label)
	}
	if _, err := snapshot.file.Seek(0, io.SeekStart); err != nil {
		return e.Wrap(e.CodeInternal, err, "rewinding private %s snapshot", snapshot.label)
	}
	return checkContext(ctx, "verifying private "+snapshot.label+" snapshot")
}

// diskEvidence observes the encryption header, aggregate digest, size, and
// optional per-block digests in one forward pass. Even if a hostile same-user
// writer defeats metadata change detection, no manifest can combine facets from
// different passes over the mutable inode.
func (snapshot *regularFileCapability) diskEvidenceContext(ctx context.Context, blockSize int, includeBlocks bool) (string, int64, *BlockManifest, error) {
	if err := checkContext(ctx, "computing canonical disk evidence"); err != nil {
		return "", 0, nil, err
	}
	if snapshot == nil || snapshot.info == nil {
		return "", 0, nil, e.New(e.CodeInternal, "private regular-file snapshot is incomplete")
	}
	blockCount := 0
	if includeBlocks {
		var err error
		blockCount, err = boundedDiskBlockCount(snapshot.info.Size(), blockSize, e.CodeInvalidCatalog)
		if err != nil {
			return "", 0, nil, err
		}
	}
	if err := snapshot.rewindAndVerifyContext(ctx); err != nil {
		return "", 0, nil, err
	}
	readSize := 128 * 1024
	if includeBlocks {
		readSize = blockSize
	}
	buffer := make([]byte, readSize)
	header := make([]byte, 0, len(luksMagic))
	aggregate := sha256.New()
	var size int64
	var manifest *BlockManifest
	if includeBlocks {
		manifest = &BlockManifest{
			BlockSizeBytes: blockSize,
			Blocks:         make([]string, 0, blockCount),
		}
	}
	for {
		if err := checkContext(ctx, "computing canonical disk evidence"); err != nil {
			return "", 0, nil, err
		}
		n, err := io.ReadFull(snapshot.file, buffer)
		if n > 0 {
			chunk := buffer[:n]
			if len(header) < len(luksMagic) {
				remaining := len(luksMagic) - len(header)
				if remaining > len(chunk) {
					remaining = len(chunk)
				}
				header = append(header, chunk[:remaining]...)
			}
			_, _ = aggregate.Write(chunk)
			size += int64(n)
			if manifest != nil {
				sum := sha256.Sum256(chunk)
				manifest.Blocks = append(manifest.Blocks, "sha256:"+hex.EncodeToString(sum[:]))
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return "", 0, nil, e.Wrap(e.CodeVerifyFailed, err,
				"reading held %s capability", snapshot.label)
		}
	}
	if err := snapshot.rewindAndVerifyContext(ctx); err != nil {
		return "", 0, nil, err
	}
	if len(header) == len(luksMagic) {
		encrypted := true
		for i := range luksMagic {
			if header[i] != luksMagic[i] {
				encrypted = false
				break
			}
		}
		if encrypted {
			return "", 0, nil, e.New(e.CodeEncryptedArtifact,
				"%s is LUKS/FDE-encrypted: byte identity attaches to the pre-encryption RAW", snapshot.label)
		}
	}
	digest := "sha256:" + hex.EncodeToString(aggregate.Sum(nil))
	if manifest != nil {
		manifest.Aggregate = digest
	}
	return digest, size, manifest, nil
}

func checkContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s", operation)
	}
	return nil
}

type assembleContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader assembleContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}
