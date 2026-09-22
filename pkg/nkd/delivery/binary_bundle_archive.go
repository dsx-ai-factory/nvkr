package delivery

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func (writer layoutWriter) addBundleLayer(ctx context.Context, captures []capturedBinary, epoch int64) (Descriptor, error) {
	tmp, err := os.CreateTemp(writer.root, ".nkd-bundle-layer-*.tmp")
	if err != nil {
		return Descriptor{}, e.Wrap(e.CodeInternal, err, "creating binary bundle layer")
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	hash := sha256.New()
	gzipWriter, err := gzip.NewWriterLevel(io.MultiWriter(tmp, hash), gzip.BestCompression)
	if err != nil {
		_ = tmp.Close()
		return Descriptor{}, err
	}
	gzipWriter.ModTime = time.Time{}
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	mtime := time.Unix(epoch, 0).UTC()
	for _, capture := range captures {
		if err := deliveryContext(ctx); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = tmp.Close()
			return Descriptor{}, err
		}
		header := &tar.Header{Name: "files/" + capture.input.Name, Mode: int64(capture.input.Mode),
			Size: capture.file.Size(), ModTime: mtime, Uid: 0, Gid: 0,
			Uname: "", Gname: "", Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = gzipWriter.Close()
			_ = tmp.Close()
			return Descriptor{}, err
		}
		if _, err := capture.file.WriteToContext(ctx, tarWriter); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = tmp.Close()
			return Descriptor{}, classifyContext(ctx, err)
		}
		if err := capture.file.AssertUnchangedContext(ctx); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = tmp.Close()
			return Descriptor{}, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		_ = tmp.Close()
		return Descriptor{}, err
	}
	if err := gzipWriter.Close(); err != nil {
		_ = tmp.Close()
		return Descriptor{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Descriptor{}, err
	}
	info, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		return Descriptor{}, err
	}
	if err := tmp.Close(); err != nil {
		return Descriptor{}, err
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	destination := filepath.Join(writer.root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return Descriptor{}, err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return Descriptor{}, err
	}
	return Descriptor{MediaType: MediaBinaryBundleLayer, Digest: digest, Size: info.Size()}, nil
}

func extractBundleLayer(ctx context.Context, held *heldfile.File, out string, bundle BinaryBundle) error {
	gzipReader, err := gzip.NewReader(io.NewSectionReader(held, 0, held.Size()))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening binary-bundle gzip layer")
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	want := make(map[string]BinaryEntry, len(bundle.Entries))
	for _, entry := range bundle.Entries {
		want["files/"+entry.Name] = entry
	}
	seen := map[string]bool{}
	for {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "reading binary-bundle tar layer")
		}
		entry, ok := want[header.Name]
		if !ok || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Size != entry.Size || uint32(header.Mode) != entry.Mode {
			return e.New(e.CodeVerifyFailed, "binary-bundle tar contains an undeclared or mismatched entry %q", header.Name)
		}
		seen[header.Name] = true
		destination := filepath.Join(out, entry.Name)
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(entry.Mode))
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "creating extracted binary %q", entry.Name)
		}
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(file, hash), contextReader{ctx: ctx, reader: tarReader}, entry.Size)
		if copyErr == nil {
			copyErr = file.Sync()
		}
		closeErr := file.Close()
		got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if copyErr != nil || closeErr != nil || written != entry.Size || got != entry.SHA256 {
			return e.New(e.CodeVerifyFailed, "extracted binary %q does not match its config", entry.Name)
		}
	}
	if len(seen) != len(want) {
		missing := make([]string, 0)
		for name := range want {
			if !seen[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return e.New(e.CodeReleaseIncomplete, "binary-bundle layer omits entries: %s", strings.Join(missing, ", "))
	}
	return nil
}
