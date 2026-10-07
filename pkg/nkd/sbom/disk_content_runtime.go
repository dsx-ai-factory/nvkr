// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

type diskContentReport struct {
	Subject            string      `json:"subject"`
	Size               int64       `json:"size"`
	Kind               string      `json:"kind"`
	Platform           string      `json:"platform"`
	Inspector          string      `json:"inspector"`
	Codec              string      `json:"codec"`
	Guestfish          string      `json:"guestfish"`
	Scanner            string      `json:"scanner"`
	PackageReader      string      `json:"packageReader"`
	MaxFilesystemBytes uint64      `json:"maxFilesystemBytes"`
	Components         []Component `json:"components"`
	Files              []DiskFile  `json:"files"`
}

// DiskContentInspection binds actual read-only filesystem/package discovery to
// one opaque sandbox result. It never declares complete acquisition coverage.
type DiskContentInspection struct {
	report        diskContentReport
	runtimeDigest string
}

func (inspection *DiskContentInspection) Binding() (subject string, size int64, kind, platform, runtime, scanner string) {
	if inspection == nil {
		return "", 0, "", "", "", ""
	}
	view := inspection.report
	return view.Subject, view.Size, view.Kind, view.Platform, inspection.runtimeDigest, view.Scanner
}

func (inspection *DiskContentInspection) Components() []Component {
	if inspection == nil {
		return nil
	}
	result := slices.Clone(inspection.report.Components)
	for index := range result {
		result[index] = cloneComponent(result[index])
	}
	return result
}

func (inspection *DiskContentInspection) Files() []DiskFile {
	if inspection == nil {
		return nil
	}
	return slices.Clone(inspection.report.Files)
}

// InspectHeldContent keeps the original capacity reservation through the
// sandbox scan. Only this owner can turn its concrete result into content proof.
func (runtime *DiskInspectionRuntime) InspectHeldContent(ctx context.Context, subject *heldfile.File, kind, platform string) (*DiskContentInspection, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	if runtime == nil || runtime.container == nil {
		return nil, e.New(e.CodeEngineCapability, "content inspection requires admitted capacity")
	}
	observation, err := runtime.container.InspectHeldContent(ctx, subject, kind, platform)
	if err != nil {
		return nil, err
	}
	return contentFromContainerObservation(observation)
}

func contentFromContainerObservation(observation *pipeline.DiskInspectionObservation) (*DiskContentInspection, error) {
	if observation == nil {
		return nil, e.New(e.CodeVerifyFailed, "disk content observation is absent")
	}
	subject, size, image, inspector, codec := observation.Binding()
	kind, guestfish, scanner, packageReader, platform, maxBytes := observation.ContentBinding()
	return decodeBoundDiskContentReport(observation.Report(), image, diskContentReport{
		Subject: subject, Size: size, Kind: kind, Platform: platform, Inspector: inspector,
		Codec: codec, Guestfish: guestfish, Scanner: scanner, PackageReader: packageReader,
		MaxFilesystemBytes: maxBytes,
	})
}

// The production caller obtains binding only from the opaque runtime observation.
// This private decoder seam tests projection, not runtime or receipt issuance.
func decodeBoundDiskContentReport(raw []byte, image string, binding diskContentReport) (*DiskContentInspection, error) {
	if len(raw) > pipeline.MaxDiskContentReportBytes {
		return nil, e.New(e.CodeEnvelopeDecode, "disk content report exceeds its budget")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeEnvelopeDecode, err, "validating disk content report")
	}
	var report diskContentReport
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return nil, e.Wrap(e.CodeEnvelopeDecode, err, "decoding disk content report")
	}
	if !canonical.IsDigest(image) || !canonical.IsDigest(binding.Guestfish) || !canonical.IsDigest(binding.Scanner) || !canonical.IsDigest(binding.PackageReader) ||
		report.Subject != binding.Subject || report.Size != binding.Size || report.Kind != binding.Kind || report.Platform != binding.Platform || report.Inspector != binding.Inspector ||
		report.Codec != binding.Codec || report.Guestfish != binding.Guestfish || report.Scanner != binding.Scanner || report.PackageReader != binding.PackageReader || report.MaxFilesystemBytes != binding.MaxFilesystemBytes {
		return nil, e.New(e.CodeVerifyFailed, "disk content report differs from its exact held subject or runtime")
	}
	if err := validateDiskContentReport(report); err != nil {
		return nil, err
	}
	return &DiskContentInspection{report: report, runtimeDigest: image}, nil
}

func validateDiskContentReport(report diskContentReport) error {
	if (len(report.Components) == 0 && len(report.Files) == 0) || len(report.Components) > maxDiskInstalledPackages || len(report.Files) > 200000 {
		return e.New(e.CodeProvenanceMissing, "disk content report lacks a bounded nonempty inventory")
	}
	for _, component := range report.Components {
		if err := validateSBOMComponent("disk content", component); err != nil {
			return err
		}
		if _, err := diskComponentClass(component); err != nil {
			return err
		}
	}
	previous := ""
	for _, file := range report.Files {
		if !fs.ValidPath(file.Path) || file.Path == "." || strings.ContainsAny(file.Path, "\x00\r\n") || file.Path <= previous || file.Size < 0 {
			return e.New(e.CodeEnvelopeDecode, "disk content report has invalid file identities")
		}
		switch file.Kind {
		case "regular":
			if !canonical.IsDigest(file.Digest) || file.Target != "" {
				return e.New(e.CodeEnvelopeDecode, "regular disk file has no exact payload identity")
			}
		case "symlink", "hardlink":
			if file.Target == "" || strings.ContainsAny(file.Target, "\x00\r\n") || file.Digest != "" || file.Size != 0 {
				return e.New(e.CodeEnvelopeDecode, "disk link has invalid descriptive metadata")
			}
		case "character-device", "block-device", "fifo":
			if file.Target != "" || file.Digest != "" || file.Size != 0 {
				return e.New(e.CodeEnvelopeDecode, "special disk file carries invented payload metadata")
			}
		default:
			return e.New(e.CodeEngineCapability, "unsupported disk file kind %q", file.Kind)
		}
		previous = file.Path
	}
	return nil
}

// RunDiskContentHelper is the fixed networkless container operation. All tools
// are authenticated before reading input; no caller command or directory is used.
func RunDiskContentHelper(ctx context.Context, inspectorDigest, codecDigest, subjectDigest string, size int64,
	kind, guestfishDigest, scannerDigest, packageReaderDigest string, maxBytes uint64, platform string, input io.Reader, output io.Writer,
) error {
	if err := validateSBOMContext(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if input == nil || output == nil || !canonical.IsDigest(subjectDigest) || size <= 0 || size > MaxDiagnosticDiskBytes || maxBytes == 0 || maxBytes > 16<<30 ||
		(platform != "linux/amd64" && platform != "linux/arm64") {
		return e.New(e.CodeUnpinnedInput, "disk content helper requires exact bounded subject, platform and filesystem budget")
	}
	var tools []*heldfile.File
	defer func() {
		for _, tool := range tools {
			_ = tool.Close()
		}
	}()
	for _, selection := range []struct{ path, pin string }{
		{"/usr/bin/virt-inspector", inspectorDigest}, {"/usr/bin/qemu-img", codecDigest}, {"/usr/bin/guestfish", guestfishDigest},
		{"/usr/local/bin/syft", scannerDigest}, {"/usr/bin/dpkg-deb", packageReaderDigest},
	} {
		if !canonical.IsDigest(selection.pin) {
			return e.New(e.CodeUnpinnedInput, "disk content helper requires every exact tool pin")
		}
		tool, err := heldfile.OpenContext(ctx, selection.path, heldfile.Options{Label: "disk-content-tool", Executable: true, MaxBytes: 512 << 20, ExpectedDigest: selection.pin})
		if err != nil {
			return e.Wrap(e.CodeUnverifiedRef, err, "authenticating disk content tool")
		}
		tools = append(tools, tool)
		image, err := elf.NewFile(tool)
		if err != nil || !diskInspectorELF(image) {
			return e.New(e.CodeEngineCapability, "disk content tool is not native Linux ELF")
		}
	}
	codec, err := assemble.OpenQemuImgCodec(ctx, "/usr/bin/qemu-img", codecDigest)
	if err != nil {
		return err
	}
	defer func() { _ = codec.Close() }()
	subject, err := holdDiskInspectionSubject(ctx, input, subjectDigest, size)
	if err != nil {
		return err
	}
	defer func() { _ = subject.Close() }()
	scanner := Scanner{Path: "/usr/local/bin/syft", Digest: scannerDigest}
	var components []Component
	var files []DiskFile
	switch kind {
	case "disk-image":
		inspection, err := inspectFilesystem(ctx, subject, "qcow2", platform, guestfishDigest, packageReaderDigest, scanner, codec, int64(maxBytes))
		if err != nil {
			return err
		}
		components, files = inspection.Components(), inspection.Files()
	case "installer-iso", "installer-content":
		inspection, err := inspectInstallerFilesystem(ctx, subject, platform, guestfishDigest, packageReaderDigest, scanner, codec, int64(maxBytes))
		if err != nil {
			return err
		}
		components, files = inspection.Components(), inspection.Files()
	case "tarball", "apt-acquisition":
		var captured *material.AcquisitionObservation
		if kind == "apt-acquisition" {
			captured, err = material.ObserveAcquisitionArchive(ctx, subject)
			if err != nil {
				return err
			}
		}
		inspection, err := inspectMaterialArchive(ctx, subject, platform, scanner, packageReaderDigest, int64(maxBytes))
		if err != nil {
			return err
		}
		components, files = inspection.Components(), inspection.Files()
		if captured != nil {
			if err := checkCapturedPackageMetadata(captured, inspection.packageMetadataByDigest, platform); err != nil {
				return err
			}
		}
	case "host-package":
		inspection, err := inspectDebianMaterial(ctx, subject, platform, scanner, packageReaderDigest, int64(maxBytes))
		if err != nil {
			return err
		}
		components, files = inspection.Components(), inspection.Files()
	case "oci-image":
		components, err = inspectContainerMaterial(ctx, subject, scanner)
		if err != nil {
			return err
		}
	default:
		return e.New(e.CodeEngineCapability, "unsupported disk content kind %q", kind)
	}
	report := diskContentReport{Subject: subject.Digest(), Size: subject.Size(), Kind: kind, Platform: platform, Inspector: inspectorDigest, Codec: codecDigest,
		Guestfish: guestfishDigest, Scanner: scannerDigest, PackageReader: packageReaderDigest, MaxFilesystemBytes: maxBytes, Components: components, Files: files}
	if err := validateDiskContentReport(report); err != nil {
		return err
	}
	raw, err := canonical.Marshal(report)
	if err != nil {
		return err
	}
	if len(raw) > pipeline.MaxDiskContentReportBytes {
		return e.New(e.CodeEnvelopeDecode, "disk content report exceeds its budget")
	}
	closeErr := errors.Join(subject.Close(), codec.Close())
	for _, tool := range tools {
		closeErr = errors.Join(closeErr, tool.Close())
	}
	if closeErr != nil {
		return e.Wrap(e.CodeInternal, closeErr, "closing disk content helper capabilities")
	}
	_, err = output.Write(raw)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "writing disk content report")
	}
	return nil
}

// The operation, not scanner-authored metadata, selects the observation class.
// This descriptive scan cannot issue a sandbox result or a factory receipt.
func inspectContainerMaterial(ctx context.Context, subject *heldfile.File, scanner Scanner) ([]Component, error) {
	inventory, err := inspectHeldDependencyReport(ctx, subject, "oci-image", scanner, true)
	if err != nil {
		return nil, err
	}
	return withDiskContentClass(inventory.Components, diskClassContainer), nil
}

func checkCapturedPackageMetadata(capture *material.AcquisitionObservation, packages map[string]diskPackageKey, platform string) error {
	manifest := capture.Description()
	if manifest.Architecture != strings.TrimPrefix(platform, "linux/") {
		return e.New(e.CodeVerifyFailed, "acquisition capture architecture differs from inspected consumer")
	}
	for _, entry := range manifest.Packages {
		actual, exists := packages[entry.Digest]
		if !exists || actual != (diskPackageKey{entry.Name, entry.Version, "deb", entry.Architecture}) {
			return e.New(e.CodeVerifyFailed, "captured package metadata differs from the pinned reader of its own exact package bytes")
		}
	}
	return nil
}
