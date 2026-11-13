// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/diskinspect"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

type diskContentReport = diskinspect.ContentReport
type DiskFile = diskinspect.DiskFile
type FilesystemInspection = diskinspect.FilesystemInspection

// DiskContentInspection binds actual read-only filesystem/package discovery to
// one opaque sandbox result. It never declares complete acquisition coverage.
type DiskContentInspection struct {
	helperName    string
	helper        pipeline.ProducedExecutableBinding
	report        diskContentReport
	runtimeDigest string
}

// ToolBinding describes the helper bound to this exact content observation.
func (inspection *DiskContentInspection) ToolBinding() (string, pipeline.ProducedExecutableBinding) {
	if inspection == nil {
		return "", pipeline.ProducedExecutableBinding{}
	}
	return inspection.helperName, inspection.helper
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
	inspection, err := decodeBoundDiskContentReport(observation.Report(), image, diskContentReport{
		Subject: subject, Size: size, Kind: kind, Platform: platform, Inspector: inspector,
		Codec: codec, Guestfish: guestfish, Scanner: scanner, PackageReader: packageReader,
		MaxFilesystemBytes: maxBytes,
	})
	if err != nil {
		return nil, err
	}
	inspection.helperName, inspection.helper = observation.ToolBinding()
	return inspection, nil
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
	return diskinspect.ValidateContentReport(report)
}

// RunDiskContentHelper delegates the fixed helper operation without issuing a host receipt.
func RunDiskContentHelper(ctx context.Context, inspectorDigest, codecDigest, subjectDigest string, size int64,
	kind, guestfishDigest, scannerDigest, packageReaderDigest string, maxBytes uint64, platform string, input io.Reader, output io.Writer,
) error {
	return diskinspect.RunDiskContentHelper(ctx, inspectorDigest, codecDigest, subjectDigest, size, kind, guestfishDigest, scannerDigest, packageReaderDigest, maxBytes, platform, input, output)
}
