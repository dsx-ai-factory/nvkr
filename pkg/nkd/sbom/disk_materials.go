package sbom

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/diskdiagnostic"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

const maxDiskNestedImages = 256

func inspectMaterialArchive(ctx context.Context, subject inspectionArchive, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64) (*FilesystemInspection, error) {
	return inspectMaterialArchiveDepth(ctx, subject, platform, scanner, packageReaderDigest, maxBytes, 0)
}

func inspectMaterialArchiveDepth(ctx context.Context, subject inspectionArchive, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64, depth int) (*FilesystemInspection, error) {
	return inspectMaterialArchiveContents(ctx, subject, platform, scanner, packageReaderDigest, maxBytes, depth, false)
}

// Empty payloads are admitted only by the Debian reader after exact package
// metadata observation; generic materials and disk roots remain nonempty.
func inspectMaterialArchiveContents(ctx context.Context, subject inspectionArchive, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64, depth int, allowEmptyDebianPayload bool) (*FilesystemInspection, error) {
	return inspectMaterialArchiveBudget(ctx, subject, platform, scanner, packageReaderDigest, maxBytes, depth, allowEmptyDebianPayload, &materialInspectionBudget{remaining: maxBytes})
}

// One operation shares expansion/member admission across sibling archives and
// package exports. A per-member bound alone does not bound their aggregate.
type materialInspectionBudget struct {
	remaining int64
	members   int
	images    int
}

func (budget *materialInspectionBudget) member() error {
	budget.members++
	if budget.members > maxDiskInstalledPackages {
		return e.New(e.CodeEngineCapability, "nested material exceeds its cumulative member budget")
	}
	return nil
}

func (budget *materialInspectionBudget) expanded(size int64) error {
	if size <= 0 || size > budget.remaining {
		return e.New(e.CodeEngineCapability, "nested material exceeds its cumulative expansion budget")
	}
	budget.remaining -= size
	return nil
}

func inspectMaterialArchiveBudget(ctx context.Context, subject inspectionArchive, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64, depth int, allowEmptyDebianPayload bool, budget *materialInspectionBudget) (*FilesystemInspection, error) {
	if depth > 2 || subject == nil || subject.Size() <= 0 || subject.Size() > maxBytes {
		return nil, e.New(e.CodeEngineCapability, "material archive exceeds bounded recursive inspection")
	}
	filesystemDepth, _ := ctx.Value(diskFilesystemDepthKey{}).(int)
	diagnosticDepth := depth + filesystemDepth
	ctx = context.WithValue(ctx, diskScanDepthKey{}, diagnosticDepth)
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
	diskdiagnostic.Emit(ctx, "file-inventory", "start", diagnosticDepth, 0)
	var files []DiskFile
	if allowEmptyDebianPayload {
		files, err = filesystemFileInventory(ctx, file, maxBytes)
	} else {
		files, err = filesystemFiles(ctx, file, maxBytes)
	}
	if err != nil {
		return nil, err
	}
	diskdiagnostic.Emit(ctx, "file-inventory", "complete", diagnosticDepth, len(files))
	diskdiagnostic.Emit(ctx, "scanner-transport", "start", diagnosticDepth, 0)
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
	diskdiagnostic.Emit(ctx, "scanner-transport", "complete", diagnosticDepth, 0)
	inventory, err := inspectGeneratedDependencies(ctx, held, scanner)
	if err != nil {
		return nil, err
	}
	result := &FilesystemInspection{subject: subject.Digest(), size: subject.Size(), scanner: scanner.Digest, exported: held.Digest(), components: withDiskContentClass(inventory.Components, diskClassFilesystem), files: files}
	// Release the scanner transport before recursively holding package/image
	// archives. The original archive remains held and authenticated throughout.
	if err := held.Close(); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "closing scanned transport")
	}
	section := io.NewSectionReader(subject, 0, subject.Size())
	reader := tar.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(section)})
	diskdiagnostic.Emit(ctx, "member-walk", "start", diagnosticDepth, 0)
	ordinal := 0
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
		ordinal++
		// Probe the held member without consuming the parent reader. archive/tar
		// owns metadata/header decoding, including PAX/GNU headers; no filename or
		// partial header grammar establishes an image format.
		offset, err := section.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "addressing held archive member")
		}
		archiveContent, err := probeImageArchive(ctx, io.NewSectionReader(subject, offset, header.Size))
		if err != nil {
			return nil, err
		}
		if archiveContent {
			diskdiagnostic.Emit(ctx, "nested-scan", "start", diagnosticDepth, ordinal)
			if err := budget.member(); err != nil {
				return nil, err
			}
			if header.Size <= 0 || header.Size > maxBytes {
				return nil, e.New(e.CodeEngineCapability, "compressed material exceeds recursive inspection budget")
			}
			member, err := createGeneratedInspectionFile(ctx, header.Size, func(destination io.Writer) error { _, err := io.Copy(destination, reader); return err })
			if err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "holding compressed archive")
			}
			digest := member.Digest()
			inspection, scanErr := inspectNestedImagesBudget(ctx, member, platform, scanner, maxBytes, depth, budget)
			closeErr := member.Close()
			if err := errors.Join(scanErr, closeErr); err != nil {
				return nil, err
			}
			if inspection != nil {
				appendContainedArchive(result, inspection, path.Clean(header.Name), digest)
			}
			diskdiagnostic.Emit(ctx, "nested-scan", "complete", diagnosticDepth, ordinal)
			continue
		}
		if !strings.HasSuffix(header.Name, ".deb") {
			continue
		}
		diskdiagnostic.Emit(ctx, "nested-scan", "start", diagnosticDepth, ordinal)
		if err := budget.member(); err != nil {
			return nil, err
		}
		if header.Size <= 0 || header.Size > 1<<30 {
			return nil, e.New(e.CodeEngineCapability, "staged package exceeds its count or byte budget")
		}
		member, err := heldfile.FromReaderContext(ctx, reader, heldfile.Options{Label: "staged-debian-package", MaxBytes: header.Size})
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "holding staged package bytes")
		}
		digest := member.Digest()
		inspection, scanErr := inspectDebianMaterialBudget(ctx, member, platform, scanner, packageReaderDigest, maxBytes, depth+1, budget)
		closeErr := member.Close()
		if err := errors.Join(scanErr, closeErr); err != nil {
			return nil, err
		}
		appendContainedArchive(result, inspection, path.Clean(header.Name), digest)
		diskdiagnostic.Emit(ctx, "nested-scan", "complete", diagnosticDepth, ordinal)
		if result.packageMetadataByDigest == nil {
			result.packageMetadataByDigest = make(map[string]diskPackageKey)
		}
		for digest, identity := range inspection.packageMetadataByDigest {
			if previous, exists := result.packageMetadataByDigest[digest]; exists && previous != identity {
				return nil, e.New(e.CodeVerifyFailed, "same package bytes produced conflicting package-reader metadata")
			}
			result.packageMetadataByDigest[digest] = identity
		}
	}
	diskdiagnostic.Emit(ctx, "member-walk", "complete", diagnosticDepth, ordinal)
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rechecking staged archive")
	}
	slices.SortFunc(result.files, func(a, b DiskFile) int { return strings.Compare(a.Path, b.Path) })
	return result, nil
}

// Containment is a description of the immediate held carrier, not an installed
// path, package origin or recursive identity. The root keeps its actual archive
// file/hash, never invented paths for files discovered inside that archive.
func appendContainedArchive(root, nested *FilesystemInspection, carrierPath, carrierDigest string) {
	for _, component := range nested.components {
		component = cloneComponent(component)
		// A deeper reader already bound this component to its immediate carrier.
		// Do not overwrite that observation or invent a concatenated ancestry.
		if !slices.ContainsFunc(component.Properties, func(property Property) bool { return property.Name == "nkd:containedIn" }) {
			component.Properties = append(component.Properties, Property{Name: "nkd:containedIn", Value: carrierPath + "@" + carrierDigest})
			slices.SortFunc(component.Properties, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
		}
		root.components = append(root.components, component)
	}
}

// Ordinary compressed data remains covered by its existing regular-file hash;
// absence of an image marker makes no image claim. Once an image format is
// indicated, decoder failures and depth limits must not silently omit it.
// Docker/OCI decoding belongs to pinned go-containerregistry and the existing
// offline closure verifier. Mixed formats are ambiguous and fail closed.
func inspectCompressedImages(ctx context.Context, subject inspectionArchive, platform string, scanner Scanner, maxBytes int64, depth int) (*FilesystemInspection, error) {
	return inspectNestedImagesBudget(ctx, subject, platform, scanner, maxBytes, depth, &materialInspectionBudget{remaining: maxBytes})
}

func inspectNestedImagesBudget(ctx context.Context, subject inspectionArchive, platform string, scanner Scanner, maxBytes int64, depth int, budget *materialInspectionBudget) (*FilesystemInspection, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	filesystemDepth, _ := ctx.Value(diskFilesystemDepthKey{}).(int)
	ctx = context.WithValue(ctx, diskScanDepthKey{}, depth+1+filesystemDepth)
	if depth > 2 || subject == nil || subject.Size() <= 0 || maxBytes <= 0 || budget == nil {
		return nil, e.New(e.CodeEngineCapability, "compressed material exceeds bounded recursive inspection")
	}
	indicated, err := probeImageArchive(ctx, io.NewSectionReader(subject, 0, subject.Size()))
	if err != nil {
		return nil, err
	}
	if !indicated {
		return nil, subject.AssertUnchangedContext(ctx)
	}
	source := bufio.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(subject, 0, subject.Size()))})
	prefix, _ := source.Peek(2)
	var expanded io.Reader = source
	if len(prefix) == 2 && prefix[0] == 0x1f && prefix[1] == 0x8b {
		compressed, err := gzip.NewReader(source)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "opening compressed image material")
		}
		defer func() { _ = compressed.Close() }()
		expanded = compressed
	}
	if budget.remaining <= 0 {
		return nil, e.New(e.CodeEngineCapability, "nested material exhausted its cumulative expansion budget")
	}
	archive, err := createGeneratedInspectionFile(ctx, min(maxBytes, budget.remaining), func(destination io.Writer) error { _, err := io.Copy(destination, expanded); return err })
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding bounded image archive")
	}
	defer func() { _ = archive.Close() }()
	if err := budget.expanded(archive.Size()); err != nil {
		return nil, err
	}
	docker, oci, err := compressedImageMarkers(ctx, archive)
	if err != nil {
		return nil, err
	}
	if oci && docker {
		return nil, e.New(e.CodeEngineCapability, "mixed OCI and Docker image archive is ambiguous")
	}
	if !docker && !oci {
		return nil, nil
	}
	if depth >= 2 {
		return nil, e.New(e.CodeEngineCapability, "compressed image material exceeds recursive inspection budget")
	}
	if oci {
		return inspectNestedOCILayout(ctx, subject, archive, platform, scanner, maxBytes, budget)
	}
	opener := func() (io.ReadCloser, error) {
		return filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(archive, 0, archive.Size()))}, nil
	}
	manifest, err := tarball.LoadManifest(opener)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding compressed Docker image manifest")
	}
	if len(manifest) == 0 || len(manifest) > maxDiskNestedImages {
		return nil, e.New(e.CodeEngineCapability, "compressed material has no bounded supported Docker image manifest")
	}
	// tarball.Image selects the first manifest entry matching a normalized tag.
	// Bind every alias to exactly one entry before scanning anything, otherwise a
	// repeated tag could select the first image twice and omit the later image.
	tags := make([]name.Tag, len(manifest))
	owners := make(map[string]int)
	for index, entry := range manifest {
		if len(entry.RepoTags) == 0 {
			return nil, e.New(e.CodeEngineCapability, "image material requires an unambiguous archive tag")
		}
		for alias, reference := range entry.RepoTags {
			tag, err := name.NewTag(reference, name.StrictValidation)
			if err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding image archive tag")
			}
			if previous, exists := owners[tag.Name()]; exists && previous != index {
				return nil, e.New(e.CodeVerifyFailed, "image material tag selects multiple manifest entries")
			}
			owners[tag.Name()] = index
			if alias == 0 {
				tags[index] = tag
			}
		}
	}
	result := &FilesystemInspection{subject: subject.Digest(), size: subject.Size(), scanner: scanner.Digest}
	seen := make(map[string]bool)
	for _, tag := range tags {
		budget.images++
		if budget.images > maxDiskNestedImages {
			return nil, e.New(e.CodeEngineCapability, "nested material exceeds its cumulative image budget")
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
		result.components = append(result.components, withDiskContentClass(inventory.Components, diskClassContainer)...)
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, err
	}
	if err := archive.AssertUnchangedContext(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// This streaming probe avoids full materialization and makes no inventory claim. Ordinary bytes
// remain covered by their outer file hash, even if a filename suggests tar/gzip.
// Only a decoded root image marker selects image inspection; that operation
// then holds, charges and strictly validates the complete archive. Both grammar
// and decompression belong to the standard library and reads remain cancelable.
func probeImageArchive(ctx context.Context, source io.Reader) (bool, error) {
	reader := bufio.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(source)})
	prefix, _ := reader.Peek(2)
	var expanded io.Reader = reader
	if len(prefix) == 2 && prefix[0] == 0x1f && prefix[1] == 0x8b {
		compressed, err := gzip.NewReader(reader)
		if err != nil {
			if ctx.Err() != nil {
				return false, e.Wrap(e.CodeTimeout, ctx.Err(), "nested material format selection canceled")
			}
			return false, nil
		}
		defer func() { _ = compressed.Close() }()
		expanded = compressed
	}
	archive := tar.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(expanded)})
	for {
		header, err := archive.Next()
		if err != nil {
			if ctx.Err() != nil {
				return false, e.Wrap(e.CodeTimeout, ctx.Err(), "nested material format selection canceled")
			}
			return false, nil
		}
		if name := path.Clean(header.Name); name == "manifest.json" || name == "oci-layout" {
			return true, nil
		}
	}
}

// archive/tar owns header decoding. This is only format selection: no entry is
// extracted and no manifest grammar is reimplemented. Inspect the whole bounded
// stream so a malformed tail or repeated format marker cannot become a fallback.
func compressedImageMarkers(ctx context.Context, archive *generatedInspectionFile) (docker, oci bool, err error) {
	reader := tar.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(archive, 0, archive.Size()))})
	for count := 0; ; count++ {
		header, err := reader.Next()
		if err == io.EOF {
			return docker, oci, nil
		}
		if err != nil {
			return false, false, e.Wrap(e.CodeVerifyFailed, err, "decoding compressed material tar headers")
		}
		if count >= 200000 {
			return false, false, e.New(e.CodeEngineCapability, "compressed material exceeds its entry budget")
		}
		var marker *bool
		switch path.Clean(header.Name) {
		case "manifest.json":
			marker = &docker
		case "oci-layout":
			marker = &oci
		default:
			continue
		}
		if *marker || header.Typeflag != tar.TypeReg {
			return false, false, e.New(e.CodeVerifyFailed, "compressed image material has duplicate or non-regular format markers")
		}
		*marker = true
	}
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
	return inspectDebianMaterialBudget(ctx, subject, platform, scanner, packageReaderDigest, maxBytes, depth, &materialInspectionBudget{remaining: maxBytes})
}

func inspectDebianMaterialBudget(ctx context.Context, subject *heldfile.File, platform string, scanner Scanner, packageReaderDigest string, maxBytes int64, depth int, budget *materialInspectionBudget) (*FilesystemInspection, error) {
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
	if budget.remaining <= 0 {
		return nil, e.New(e.CodeEngineCapability, "package export exhausted its cumulative expansion budget")
	}
	filesystem, err := createGeneratedInspectionFile(ctx, min(maxBytes, budget.remaining), func(destination io.Writer) error {
		return runBoundedExport(ctx, tool, []string{"--fsys-tarfile", child}, options, destination, min(maxBytes, budget.remaining), subject)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = filesystem.Close() }()
	if err := budget.expanded(filesystem.Size()); err != nil {
		return nil, err
	}
	if diagnostic.Len() != 0 {
		return nil, e.New(e.CodeEnvelopeDecode, "package export returned diagnostics")
	}
	inspection, err := inspectMaterialArchiveBudget(ctx, filesystem, platform, scanner, packageReaderDigest, maxBytes, depth+1, true, budget)
	if err != nil {
		return nil, err
	}
	component := Component{Type: "library", Name: fields[0], Version: fields[1], Properties: []Property{
		{Name: diskContentClassProperty, Value: diskClassDebianArchive}, {Name: "nkd:packageArchitecture", Value: fields[2]}, {Name: "nkd:packageFormat", Value: "deb"},
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
