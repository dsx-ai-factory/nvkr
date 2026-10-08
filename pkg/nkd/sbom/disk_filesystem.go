// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/diskdiagnostic"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// DiskFile describes an observed regular file or link. Paths are relative to the
// guest root and never resolved on the host. Link targets remain uninterpreted.
type DiskFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Digest string `json:"digest,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Target string `json:"target,omitempty"`
}

// FilesystemInspection is issued after an exact held image has been exported
// read-only by pinned guestfish and scanned offline. It observes this image, not
// the origin of its contents or complete acquisition coverage.
type FilesystemInspection struct {
	subject                      string
	size                         int64
	guestfish, scanner, exported string
	components                   []Component
	files                        []DiskFile
	// Only dpkg-deb observations of original held package bytes populate this
	// map. Aggregate scanner metadata cannot authenticate a capture entry.
	packageMetadataByDigest map[string]diskPackageKey
}

func (inspection *FilesystemInspection) Binding() (subject string, size int64, guestfish, scanner, exported string) {
	if inspection == nil {
		return "", 0, "", "", ""
	}
	return inspection.subject, inspection.size, inspection.guestfish, inspection.scanner, inspection.exported
}

func (inspection *FilesystemInspection) Components() []Component {
	if inspection == nil {
		return nil
	}
	result := slices.Clone(inspection.components)
	for index := range result {
		result[index] = cloneComponent(result[index])
	}
	return result
}

func (inspection *FilesystemInspection) Files() []DiskFile {
	if inspection == nil {
		return nil
	}
	return slices.Clone(inspection.files)
}

// inspectFilesystem operates inside the admitted networkless inspection image.
// guestfish owns filesystem decoding; go-containerregistry owns the scanner
// transport image; Syft owns package discovery. No guest executable is run and
// no guest tar entry is extracted into the host filesystem.
func inspectFilesystem(ctx context.Context, subject inspectionArchive, format, platform, guestfishDigest, packageReaderDigest string,
	scanner Scanner, codec *assemble.QemuImgCodec, maxBytes int64,
) (*FilesystemInspection, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	depth, _ := ctx.Value(diskScanDepthKey{}).(int)
	ordinal, _ := ctx.Value(diskFilesystemOrdinalKey{}).(int)
	if subject == nil || subject.Size() <= 0 || maxBytes <= 0 || maxBytes > 16<<30 ||
		(platform != "linux/amd64" && platform != "linux/arm64") || !canonical.IsDigest(guestfishDigest) || !canonical.IsDigest(scanner.Digest) {
		return nil, e.New(e.CodeEngineCapability, "filesystem inspection requires a bounded exact subject and complete native tool pins")
	}
	if format != "qcow2" && format != "raw" && format != "iso" {
		return nil, e.New(e.CodeEngineCapability, "filesystem export does not support format %q", format)
	}
	if format == "qcow2" {
		held, ok := subject.(*heldfile.File)
		if !ok {
			return nil, e.New(e.CodeEngineCapability, "QCOW2 inspection requires the original held subject")
		}
		if err := codec.ValidateHeldEnvelope(ctx, format, held); err != nil {
			return nil, err
		}
	}
	guestfish, err := heldfile.OpenContext(ctx, "/usr/bin/guestfish", heldfile.Options{Label: "filesystem-exporter", Executable: true, MaxBytes: 512 << 20, ExpectedDigest: guestfishDigest})
	if err != nil {
		return nil, e.Wrap(e.CodeUnverifiedRef, err, "holding filesystem exporter")
	}
	defer func() { _ = guestfish.Close() }()
	image, err := elf.NewFile(guestfish)
	if err != nil || !diskInspectorELF(image) {
		return nil, e.New(e.CodeEngineCapability, "filesystem exporter is not native Linux ELF")
	}
	workspace, err := os.MkdirTemp("/tmp", "nkd-fs-*")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating filesystem scan workspace")
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	// libguestfs reopens the supplied path. This private read-only copy is made
	// only inside the admitted container and is rehashed before accepting output.
	snapshot := filepath.Join(workspace, "subject")
	diskdiagnostic.Emit(ctx, "filesystem-staging", "start", depth, ordinal)
	file, err := os.OpenFile(snapshot, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "staging filesystem subject")
	}
	_, copyErr := subject.WriteToContext(ctx, file)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "copying filesystem subject")
	}
	diskdiagnostic.Emit(ctx, "filesystem-staging", "complete", depth, ordinal)
	exported := filepath.Join(workspace, "root.tar")
	output, err := os.OpenFile(exported, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o400)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating bounded filesystem export")
	}
	defer func() { _ = output.Close() }()
	guestFormat := format
	if format == "iso" {
		guestFormat = "raw"
	}
	args := []string{"--ro", "--format=" + guestFormat, "-a", snapshot}
	if format == "qcow2" {
		args = append(args, "-i")
	} else {
		args = append(args, "-m", "/dev/sda")
	}
	if format == "iso" {
		// Material kind "raw" is not itself an installer-format assertion.
		// The pinned filesystem owner must identify the supported ISO filesystem
		// before an exported SquashFS member can count as installer evidence.
		var filesystem, diagnostic boundedScanBuffer
		diskdiagnostic.Emit(ctx, "filesystem-probe", "start", depth, ordinal)
		probe := append(slices.Clone(args), "vfs-type", "/dev/sda")
		if err := heldfile.RunContext(ctx, guestfish, probe, heldfile.RunOptions{Stdout: &filesystem, Stderr: &diagnostic,
			Dir: workspace, Env: diskInspectorEnvironment(workspace), TerminationGrace: time.Second}); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "identifying installer filesystem")
		}
		if err := validateInstallerFilesystemType(filesystem.String(), diagnostic.String()); err != nil {
			return nil, err
		}
		diskdiagnostic.Emit(ctx, "filesystem-probe", "complete", depth, ordinal)
	}
	args = append(args, "tar-out", "/", "-")
	var diagnostic boundedScanBuffer
	// guestfish 1.52 auto-launches and mounts -i/-m before executing an event
	// command. Keep its read-only mounting semantics; first exported bytes mark
	// extraction, not an invented launch_done callback or an appliance probe.
	diskdiagnostic.Emit(ctx, "export-startup", "start", depth, ordinal)
	extraction := &diskExtractionWriter{ctx: ctx, writer: output, depth: depth, ordinal: ordinal}
	if err := runBoundedExport(ctx, guestfish, args, heldfile.RunOptions{Stderr: &diagnostic,
		Dir: workspace, Env: diskInspectorEnvironment(workspace), TerminationGrace: time.Second}, extraction, maxBytes); err != nil {
		return nil, err
	}
	diskdiagnostic.Emit(ctx, "filesystem-extraction", "complete", depth, ordinal)
	if diagnostic.Len() != 0 {
		return nil, e.New(e.CodeEnvelopeDecode, "filesystem export returned diagnostics")
	}
	diskdiagnostic.Emit(ctx, "subject-revalidation", "start", depth, ordinal)
	if err := recheckFilesystemSnapshot(ctx, snapshot, subject); err != nil {
		return nil, err
	}
	rootDigest, err := filesystemArchiveDigest(ctx, output)
	if err != nil {
		return nil, err
	}
	diskdiagnostic.Emit(ctx, "subject-revalidation", "complete", depth, ordinal)
	diskdiagnostic.Emit(ctx, "scanner-transport", "start", depth, ordinal)
	held, err := createGeneratedInspectionFile(ctx, maxBytes, func(destination io.Writer) error {
		_, err := io.Copy(destination, filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(output, 0, maxBytes))})
		return err
	})
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding bounded filesystem scan transport")
	}
	defer func() { _ = held.Close() }()
	if held.Digest() != rootDigest {
		return nil, e.New(e.CodeVerifyFailed, "filesystem export changed while staging scan")
	}
	diskdiagnostic.Emit(ctx, "scanner-transport", "complete", depth, ordinal)
	inventory, err := inspectMaterialArchive(ctx, held, platform, scanner, packageReaderDigest, maxBytes)
	if err != nil {
		return nil, err
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rechecking filesystem source")
	}
	currentDigest, err := filesystemArchiveDigest(ctx, output)
	if err != nil || currentDigest != rootDigest {
		return nil, e.New(e.CodeVerifyFailed, "exported filesystem changed during scanning")
	}
	if format == "iso" {
		nested, err := inspectInstallerMembers(ctx, output, platform, guestfishDigest, packageReaderDigest, scanner, codec, maxBytes)
		if err != nil {
			return nil, err
		}
		inventory.components = append(inventory.components, nested.components...)
	}
	return &FilesystemInspection{subject: subject.Digest(), size: subject.Size(), guestfish: guestfishDigest,
		scanner: scanner.Digest, exported: held.Digest(), components: inventory.components, files: inventory.files}, nil
}

// The export owner, not debug-log text, identifies the first filesystem bytes.
// Before that boundary export-startup includes appliance startup and auto-mount.
type diskExtractionWriter struct {
	ctx            context.Context
	writer         io.Writer
	started        bool
	depth, ordinal int
}

func (writer *diskExtractionWriter) Write(raw []byte) (int, error) {
	if len(raw) > 0 && !writer.started {
		writer.started = true
		diskdiagnostic.Emit(writer.ctx, "export-startup", "complete", writer.depth, writer.ordinal)
		diskdiagnostic.Emit(writer.ctx, "filesystem-extraction", "start", writer.depth, writer.ordinal)
	}
	return writer.writer.Write(raw)
}

// runBoundedExport uses the standard library pipe and CopyN to enforce the
// expansion limit before a pinned child can fill memory-backed scratch. It owns
// and joins the copy goroutine and stops the child on overflow or cancellation.
func runBoundedExport(ctx context.Context, tool *heldfile.File, args []string, options heldfile.RunOptions, output io.Writer, maxBytes int64, inputs ...heldfile.Descriptor) error {
	if ctx == nil || tool == nil || output == nil || maxBytes <= 0 || maxBytes > 16<<30 {
		return e.New(e.CodeEngineCapability, "export requires an admitted byte bound")
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	finished := make(chan error, 1)
	go func() {
		count, err := io.CopyN(output, reader, maxBytes+1)
		if count > maxBytes {
			err = e.New(e.CodeEngineCapability, "filesystem export exceeds its admitted byte budget")
		}
		if err == io.EOF && count > 0 {
			err = nil
		}
		if err != nil {
			cancel()
		}
		_ = reader.CloseWithError(err)
		finished <- err
	}()
	options.Stdout = writer
	runErr := heldfile.RunContext(ctx, tool, args, options, inputs...)
	_ = writer.CloseWithError(runErr)
	copyErr := <-finished
	if parent.Err() != nil {
		return e.Wrap(e.CodeTimeout, parent.Err(), "filesystem export canceled")
	}
	if copyErr != nil {
		var coded *e.Error
		if errors.As(copyErr, &coded) {
			return copyErr
		}
	}
	if err := errors.Join(runErr, copyErr); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "exporting bounded read-only filesystem")
	}
	return nil
}

func inspectInstallerFilesystem(ctx context.Context, subject *heldfile.File, platform, guestfishDigest, packageReaderDigest string, scanner Scanner, codec *assemble.QemuImgCodec, maxBytes int64) (*FilesystemInspection, error) {
	return inspectFilesystem(ctx, subject, "iso", platform, guestfishDigest, packageReaderDigest, scanner, codec, maxBytes)
}

func validateInstallerFilesystemType(filesystem, diagnostic string) error {
	if diagnostic != "" || strings.TrimSpace(filesystem) != "iso9660" {
		return e.New(e.CodeEngineCapability, "installer material is not the supported ISO9660 filesystem")
	}
	return nil
}

// The supported installer format contains explicitly named squashfs members.
// Their compression/filesystem grammar is decoded by libguestfs, not by Go.
// Every member is scanned; an absent or unreadable base filesystem fails closed.
func inspectInstallerMembers(ctx context.Context, archive *os.File, platform, guestfishDigest, packageReaderDigest string, scanner Scanner, codec *assemble.QemuImgCodec, maxBytes int64) (*FilesystemInspection, error) {
	depth, _ := ctx.Value(diskScanDepthKey{}).(int)
	ordinal := 0
	return inspectInstallerMembersWith(ctx, archive, func(member inspectionArchive) (*FilesystemInspection, error) {
		ordinal++
		memberCtx := context.WithValue(ctx, diskScanDepthKey{}, depth+1)
		memberCtx = context.WithValue(memberCtx, diskFilesystemDepthKey{}, depth+1)
		memberCtx = context.WithValue(memberCtx, diskFilesystemOrdinalKey{}, ordinal)
		return inspectFilesystem(memberCtx, member, "raw", platform, guestfishDigest, packageReaderDigest, scanner, codec, maxBytes)
	})
}

type diskFilesystemOrdinalKey struct{}
type diskFilesystemDepthKey struct{}

const maxDiskInstallerMembers = 32

// The production caller supplies the concrete filesystem owner. This private
// seam tests held SquashFS member/correspondence projection, not native decoding.
func inspectInstallerMembersWith(ctx context.Context, archive *os.File, inspect func(inspectionArchive) (*FilesystemInspection, error)) (*FilesystemInspection, error) {
	info, err := archive.Stat()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening installer filesystem export")
	}
	reader := tar.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(archive, 0, info.Size()))})
	result := &FilesystemInspection{}
	count := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading installer export")
		}
		if header.Typeflag != tar.TypeReg || !strings.HasSuffix(header.Name, ".squashfs") {
			continue
		}
		count++
		if count > maxDiskInstallerMembers || header.Size <= 0 || header.Size > MaxDiagnosticDiskBytes {
			return nil, e.New(e.CodeEngineCapability, "installer base filesystem exceeds its supported budget")
		}
		member, err := createGeneratedInspectionFile(ctx, header.Size, func(destination io.Writer) error { _, err := io.Copy(destination, reader); return err })
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "holding installer base filesystem")
		}
		digest := member.Digest()
		inspection, scanErr := inspect(member)
		closeErr := member.Close()
		if err := errors.Join(scanErr, closeErr); err != nil {
			return nil, err
		}
		appendContainedArchive(result, inspection, path.Clean(header.Name), digest)
	}
	if count == 0 {
		return nil, e.New(e.CodeEngineCapability, "installer has no supported base squashfs filesystem")
	}
	return result, nil
}

func filesystemArchiveDigest(ctx context.Context, file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "observing filesystem archive")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(file, 0, info.Size()))}); err != nil {
		if ctx.Err() != nil {
			return "", e.Wrap(e.CodeTimeout, ctx.Err(), "hashing filesystem archive canceled")
		}
		return "", e.Wrap(e.CodeVerifyFailed, err, "hashing filesystem archive")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func recheckFilesystemSnapshot(ctx context.Context, path string, original inspectionArchive) error {
	file, err := os.Open(path)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "filesystem source changed during export")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != original.Size() {
		return e.New(e.CodeVerifyFailed, "filesystem source size changed during export")
	}
	digest, err := filesystemArchiveDigest(ctx, file)
	if err != nil {
		return err
	}
	if digest != original.Digest() {
		return e.New(e.CodeVerifyFailed, "filesystem source bytes changed during export")
	}
	return nil
}

func filesystemFiles(ctx context.Context, file *os.File, maxBytes int64) ([]DiskFile, error) {
	files, err := filesystemFileInventory(ctx, file, maxBytes)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, e.New(e.CodeProvenanceMissing, "filesystem inspection returned no files")
	}
	return files, nil
}

// The same bounded tar decoder serves disk roots and Debian data exports. Only
// the package reader may pair an empty data filesystem with observed metadata;
// filesystemFiles retains the nonempty requirement for all other subjects.
func filesystemFileInventory(ctx context.Context, file *os.File, maxBytes int64) ([]DiskFile, error) {
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > maxBytes {
		return nil, e.New(e.CodeVerifyFailed, "filesystem export is absent or oversized")
	}
	reader := tar.NewReader(filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(file, 0, info.Size()))})
	seen := make(map[string]bool)
	var result []DiskFile
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading exported filesystem")
		}
		if len(seen) >= 200000 {
			return nil, e.New(e.CodeEngineCapability, "filesystem inventory exceeds its file budget")
		}
		filename := strings.TrimPrefix(header.Name, "./")
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if filename == "" || filename == "." || !fs.ValidPath(filename) || path.Clean(filename) != filename || strings.ContainsAny(filename, "\x00\r\n") || seen[filename] {
			return nil, e.New(e.CodeVerifyFailed, "filesystem export contains unsafe or duplicate paths")
		}
		seen[filename] = true
		entry := DiskFile{Path: filename}
		switch header.Typeflag {
		case tar.TypeReg:
			hash := sha256.New()
			count, err := io.Copy(hash, reader)
			if err != nil || count != header.Size {
				return nil, e.New(e.CodeVerifyFailed, "filesystem regular member is truncated")
			}
			entry.Kind = "regular"
			entry.Size = count
			entry.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
		case tar.TypeSymlink:
			entry.Kind = "symlink"
			entry.Target = header.Linkname
		case tar.TypeLink:
			entry.Kind = "hardlink"
			entry.Target = header.Linkname
		case tar.TypeChar:
			entry.Kind = "character-device"
		case tar.TypeBlock:
			entry.Kind = "block-device"
		case tar.TypeFifo:
			entry.Kind = "fifo"
		default:
			return nil, e.New(e.CodeEngineCapability, "filesystem export contains unsupported special files")
		}
		result = append(result, entry)
	}
	slices.SortFunc(result, func(a, b DiskFile) int { return strings.Compare(a.Path, b.Path) })
	return result, nil
}

type filesystemContextReader struct {
	ctx context.Context
	io.ReadCloser
}

// Syft owns Debian version and architecture parsing. Its native report retains
// fields which the CycloneDX encoding deliberately omits. Retain native location
// coordinates through capability admission, then project semantic package identity;
// locations describe scanner input, not installed origin or a container owner.
func filesystemDependenciesFromJSON(raw []byte, scannerDigest string) (DependencyInventory, error) {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeEnvelopeDecode, err, "validating filesystem package report")
	}
	var report struct {
		Artifacts []struct {
			Name      string `json:"name"`
			Version   string `json:"version"`
			Type      string `json:"type"`
			PURL      string `json:"purl"`
			Locations []struct {
				Path       string `json:"path"`
				AccessPath string `json:"accessPath"`
				LayerID    string `json:"layerID"`
			} `json:"locations"`
			Metadata struct {
				Architecture string `json:"architecture"`
			} `json:"metadata"`
		} `json:"artifacts"`
		Schema struct {
			Version string `json:"version"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeEnvelopeDecode, err, "decoding filesystem package report")
	}
	// Exact native schema emitted by pinned Syft v1.51.1, verified against its
	// internal.JSONSchemaVersion. New schemas require an explicit adapter update.
	if report.Schema.Version != "16.1.10" || len(report.Artifacts) > maxDiskInstalledPackages {
		return DependencyInventory{}, e.New(e.CodeProvenanceMissing, "filesystem scanner returned no bounded package inventory")
	}
	result := DependencyInventory{Method: "syft-cyclonedx/v1", ScannerDigest: scannerDigest}
	for _, artifact := range report.Artifacts {
		component := Component{Type: "library", Name: artifact.Name, Version: artifact.Version, PURL: artifact.PURL,
			Properties: []Property{{Name: "nkd:packageFormat", Value: artifact.Type}}}
		if artifact.Type == "deb" {
			// Syft v1.51.1's dpkg-db-cataloger deliberately searches **/lib/dpkg.
			// A nested database has no independently held filesystem/image owner
			// here. This shared decoder serves filesystem and container scans;
			// refuse nested DBs in either rather than guessing their child owner.
			for _, location := range artifact.Locations {
				for _, coordinate := range []string{location.Path, location.AccessPath} {
					clean := path.Clean("/" + coordinate)
					for _, database := range []string{"dpkg/status", "dpkg/status.d/", "opkg/status", "opkg/info/"} {
						marker := "/lib/" + database
						fragment := strings.HasSuffix(marker, "/")
						if !strings.HasSuffix(clean, marker) && !(fragment && strings.Contains(clean, marker)) {
							continue
						}
						rooted := false
						for _, prefix := range []string{"/var", "/usr", ""} {
							rooted = rooted || clean == prefix+marker || fragment && strings.HasPrefix(clean, prefix+marker)
						}
						if !rooted {
							return DependencyInventory{}, e.New(e.CodeEngineCapability, "filesystem scanner found an unsupported nested package database at %q; separate held root/container inspection is required", coordinate)
						}
					}
				}
			}
			if artifact.Metadata.Architecture == "" || artifact.Version == "" {
				return DependencyInventory{}, e.New(e.CodeProvenanceMissing, "Debian filesystem package lacks exact version or architecture")
			}
			component.Properties = append(component.Properties, Property{Name: "nkd:packageArchitecture", Value: artifact.Metadata.Architecture})
			slices.SortFunc(component.Properties, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
		}
		if err := validateSBOMComponent("filesystem dependency", component); err != nil {
			return DependencyInventory{}, err
		}
		result.Components = append(result.Components, component)
	}
	return result, nil
}

func (reader filesystemContextReader) Read(raw []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.ReadCloser.Read(raw)
}
