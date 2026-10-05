package sbom

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func inspectMaterialArchive(ctx context.Context, subject inspectionArchive, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64) (*FilesystemInspection, error) {
	return inspectMaterialArchiveDepth(ctx, subject, platform, scanner, packageReaderDigest, maxBytes, 0)
}

func inspectMaterialArchiveDepth(ctx context.Context, subject inspectionArchive, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64, depth int) (*FilesystemInspection, error) {
	if depth > 2 || subject == nil || subject.Size() <= 0 || subject.Size() > maxBytes {
		return nil, e.New(e.CodeEngineCapability, "material archive exceeds bounded recursive inspection")
	}
	workspace, err := os.MkdirTemp("/tmp", "nkd-material-*")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating material inspection workspace")
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	filename := filepath.Join(workspace, "root.tar")
	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "staging material inspection")
	}
	defer func() { _ = file.Close() }()
	if _, err := subject.WriteToContext(ctx, file); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "copying held material archive")
	}
	files, err := filesystemFiles(ctx, file, maxBytes)
	if err != nil {
		return nil, err
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(subject, 0, subject.Size()))}, nil
	})
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "encoding material filesystem layer")
	}
	image, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "forming material scan transport")
	}
	configuration, err := image.ConfigFile()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading material scan platform")
	}
	configuration.OS = "linux"
	configuration.Architecture = strings.TrimPrefix(platform, "linux/")
	image, err = mutate.ConfigFile(image, configuration)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "binding material scan platform")
	}
	held, err := createGeneratedInspectionFile(ctx, maxBytes, func(destination io.Writer) error {
		return tarball.Write(name.MustParseReference("nkd.invalid/material:scan"), image, destination)
	})
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding bounded material scan transport")
	}
	defer func() { _ = held.Close() }()
	inventory, err := inspectGeneratedDependencies(ctx, held, scanner)
	if err != nil {
		return nil, err
	}
	result := &FilesystemInspection{subject: subject.Digest(), size: subject.Size(), scanner: scanner.Digest, exported: held.Digest(), components: inventory.Components, files: files}
	// Release the scanner transport before recursively holding package/image
	// archives. The original archive remains held and authenticated throughout.
	if err := held.Close(); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "closing scanned transport")
	}
	reader := tar.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(subject, 0, subject.Size()))})
	count := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading staged archive members")
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if strings.HasSuffix(header.Name, ".tgz") || strings.HasSuffix(header.Name, ".tar.gz") {
			if depth >= 2 || header.Size <= 0 || header.Size > maxBytes {
				return nil, e.New(e.CodeEngineCapability, "compressed material exceeds recursive inspection budget")
			}
			member, err := createGeneratedInspectionFile(ctx, header.Size, func(destination io.Writer) error { _, err := io.Copy(destination, reader); return err })
			if err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "holding compressed archive")
			}
			inspection, scanErr := inspectCompressedImages(ctx, member, platform, scanner, maxBytes)
			closeErr := member.Close()
			if err := errors.Join(scanErr, closeErr); err != nil {
				return nil, err
			}
			result.components = append(result.components, inspection.components...)
			continue
		}
		if !strings.HasSuffix(header.Name, ".deb") {
			continue
		}
		count++
		if count > maxDiskInstalledPackages || header.Size <= 0 || header.Size > 1<<30 {
			return nil, e.New(e.CodeEngineCapability, "staged package exceeds its count or byte budget")
		}
		member, err := heldfile.FromReaderContext(ctx, reader, heldfile.Options{Label: "staged-debian-package", MaxBytes: header.Size})
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "holding staged package bytes")
		}
		inspection, scanErr := inspectDebianMaterialDepth(ctx, member, platform, scanner, packageReaderDigest, maxBytes, depth+1)
		closeErr := member.Close()
		if err := errors.Join(scanErr, closeErr); err != nil {
			return nil, err
		}
		result.components = append(result.components, inspection.components...)
		if result.packageMetadataByDigest == nil {
			result.packageMetadataByDigest = make(map[string]diskPackageKey)
		}
		for digest, identity := range inspection.packageMetadataByDigest {
			if previous, exists := result.packageMetadataByDigest[digest]; exists && previous != identity {
				return nil, e.New(e.CodeVerifyFailed, "same package bytes produced conflicting package-reader metadata")
			}
			result.packageMetadataByDigest[digest] = identity
		}
		for _, file := range inspection.files {
			file.Path = "packages/" + strings.TrimPrefix(header.Name, "./") + "/" + file.Path
			result.files = append(result.files, file)
		}
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rechecking staged archive")
	}
	slices.SortFunc(result.files, func(a, b DiskFile) int { return strings.Compare(a.Path, b.Path) })
	return result, nil
}

// Docker archive parsing and image selection belong to the existing pinned
// go-containerregistry library. Scan every image, not whichever manifest occurs
// first. Gzip expansion is bounded by the held-file owner before image decoding.
func inspectCompressedImages(ctx context.Context, subject inspectionArchive, platform string, scanner Scanner, maxBytes int64) (*FilesystemInspection, error) {
	compressed, err := gzip.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(subject, 0, subject.Size()))})
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening compressed image material")
	}
	defer func() { _ = compressed.Close() }()
	archive, err := createGeneratedInspectionFile(ctx, maxBytes, func(destination io.Writer) error { _, err := io.Copy(destination, compressed); return err })
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding bounded image archive")
	}
	defer func() { _ = archive.Close() }()
	opener := func() (io.ReadCloser, error) {
		return filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(archive, 0, archive.Size()))}, nil
	}
	manifest, err := tarball.LoadManifest(opener)
	if err != nil || len(manifest) == 0 || len(manifest) > 256 {
		return nil, e.Wrap(e.CodeEngineCapability, err, "compressed material is not a bounded supported Docker image archive")
	}
	result := &FilesystemInspection{subject: subject.Digest(), size: subject.Size(), scanner: scanner.Digest}
	seen := make(map[string]bool)
	for _, entry := range manifest {
		if len(entry.RepoTags) == 0 {
			return nil, e.New(e.CodeEngineCapability, "image material requires an unambiguous archive tag")
		}
		tag, err := name.NewTag(entry.RepoTags[0], name.StrictValidation)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding image archive tag")
		}
		image, err := tarball.Image(opener, &tag)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding image material")
		}
		configuration, err := image.ConfigFile()
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading image material platform")
		}
		if configuration.OS != "linux" || configuration.Architecture != strings.TrimPrefix(platform, "linux/") {
			return nil, e.New(e.CodeVerifyFailed, "image material platform differs from consumer")
		}
		digest, err := image.Digest()
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "hashing selected image material")
		}
		if seen[digest.String()] {
			continue
		}
		seen[digest.String()] = true
		held, err := createGeneratedInspectionFile(ctx, maxBytes, func(destination io.Writer) error { return tarball.Write(tag, image, destination) })
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "sealing image scan transport")
		}
		inventory, scanErr := inspectGeneratedDependencies(ctx, held, scanner)
		closeErr := held.Close()
		if err := errors.Join(scanErr, closeErr); err != nil {
			return nil, err
		}
		result.components = append(result.components, inventory.Components...)
	}
	return result, nil
}

func writeBoundedArchive(ctx context.Context, output io.Writer, maxBytes int64, produce func(io.Writer) error) error {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		count, err := io.CopyN(output, filesystemContextReader{ctx: ctx, ReadCloser: reader}, maxBytes+1)
		if count > maxBytes {
			err = e.New(e.CodeEngineCapability, "scanner transport exceeds admitted byte budget")
		}
		if err == io.EOF && count > 0 {
			err = nil
		}
		_ = reader.CloseWithError(err)
		done <- err
	}()
	writeErr := produce(writer)
	_ = writer.CloseWithError(writeErr)
	copyErr := <-done
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "scanner transport canceled")
	}
	if copyErr != nil {
		var coded *e.Error
		if errors.As(copyErr, &coded) {
			return copyErr
		}
	}
	if err := errors.Join(writeErr, copyErr); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "writing bounded scanner transport")
	}
	return nil
}

func inspectDebianMaterial(ctx context.Context, subject *heldfile.File, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64) (*FilesystemInspection, error) {
	return inspectDebianMaterialDepth(ctx, subject, platform, scanner, packageReaderDigest, maxBytes, 0)
}

func inspectDebianMaterialDepth(ctx context.Context, subject *heldfile.File, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64, depth int) (*FilesystemInspection, error) {
	if subject == nil || subject.Size() <= 0 || subject.Size() > 1<<30 || depth > 1 || !canonical.IsDigest(packageReaderDigest) {
		return nil, e.New(e.CodeEngineCapability, "Debian material inspection requires bounded bytes and an exact package reader")
	}
	tool, err := heldfile.OpenContext(ctx, "/usr/bin/dpkg-deb", heldfile.Options{Label: "package-reader", Executable: true, MaxBytes: 512 << 20, ExpectedDigest: packageReaderDigest})
	if err != nil {
		return nil, e.Wrap(e.CodeUnverifiedRef, err, "holding package archive reader")
	}
	defer func() { _ = tool.Close() }()
	child, err := heldfile.ChildPath(1)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "addressing package archive")
	}
	var metadata, diagnostic boundedScanBuffer
	options := heldfile.RunOptions{Stdout: &metadata, Stderr: &diagnostic, Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "LC_ALL=C", "TZ=UTC"}, TerminationGrace: time.Second}
	if err := heldfile.RunContext(ctx, tool, []string{"--show", "--showformat=${Package}\t${Version}\t${Architecture}\n", child}, options, subject); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading authoritative Debian package metadata")
	}
	fields := strings.Split(strings.TrimSuffix(metadata.String(), "\n"), "\t")
	if len(fields) != 3 || diagnostic.Len() != 0 {
		return nil, e.New(e.CodeEnvelopeDecode, "package reader returned malformed metadata or diagnostics")
	}
	for _, field := range fields {
		if !canonicalText(field) || strings.ContainsAny(field, "\t\r\n ") {
			return nil, e.New(e.CodeEnvelopeDecode, "package reader returned non-canonical fields")
		}
	}
	architecture := strings.TrimPrefix(platform, "linux/")
	if fields[2] != architecture && fields[2] != "all" {
		return nil, e.New(e.CodeVerifyFailed, "Debian package architecture differs from the consumer")
	}
	// Bound the stream while the child is alive. A descriptor-size check after
	// process exit would let a compressed package exhaust the container first.
	options.Stderr = &diagnostic
	filesystem, err := createGeneratedInspectionFile(ctx, maxBytes, func(destination io.Writer) error {
		return runBoundedExport(ctx, tool, []string{"--fsys-tarfile", child}, options, destination, maxBytes, subject)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = filesystem.Close() }()
	if diagnostic.Len() != 0 {
		return nil, e.New(e.CodeEnvelopeDecode, "package export returned diagnostics")
	}
	inspection, err := inspectMaterialArchiveDepth(ctx, filesystem, platform, scanner, packageReaderDigest, maxBytes, depth+1)
	if err != nil {
		return nil, err
	}
	component := Component{Type: "library", Name: fields[0], Version: fields[1], Properties: []Property{
		{Name: "nkd:packageArchitecture", Value: fields[2]}, {Name: "nkd:packageFormat", Value: "deb"},
	}}
	inspection.components = append(inspection.components, component)
	if inspection.packageMetadataByDigest == nil {
		inspection.packageMetadataByDigest = make(map[string]diskPackageKey)
	}
	inspection.packageMetadataByDigest[subject.Digest()] = diskPackageKey{fields[0], fields[1], "deb", fields[2]}
	inspection.subject = subject.Digest()
	inspection.size = subject.Size()
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rechecking Debian material")
	}
	return inspection, nil
}
