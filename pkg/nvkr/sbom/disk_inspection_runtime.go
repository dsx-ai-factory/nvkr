// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/diskinspect"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// DiskInspectionRuntime admits the entire local OCI inspection closure and its
// concrete sandbox, rather than accepting callers' isolation promises. Factory
// remains the owner of the original output/Plan/Run binding and live lifetime.
type DiskInspectionRuntime struct {
	container *pipeline.DiskInspectionContainer
}

// OpenDiskInspectionRuntime checks an already-local exact image and observes
// the caller-selected trusted Linux runtime before touching disk input/output.
// The runtime is held and rechecked for freshness, not authenticated against an
// independently supplied executable or daemon pin.
func OpenDiskInspectionRuntime(ctx context.Context, selection pipeline.DiskInspectionSelection) (*DiskInspectionRuntime, error) {
	container, err := pipeline.OpenDiskInspectionContainer(ctx, selection)
	if err != nil {
		return nil, err
	}
	return &DiskInspectionRuntime{container: container}, nil
}

func (runtime *DiskInspectionRuntime) BindingContext(ctx context.Context) (image, inspector, codec string, err error) {
	if runtime == nil {
		return "", "", "", e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	return runtime.container.BindingContext(ctx)
}

func (runtime *DiskInspectionRuntime) Close() error {
	if runtime == nil {
		return nil
	}
	return runtime.container.Close()
}

func (runtime *DiskInspectionRuntime) SelectionContext(ctx context.Context) (pipeline.DiskInspectionSelection, error) {
	if runtime == nil {
		return pipeline.DiskInspectionSelection{}, e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	return runtime.container.SelectionContext(ctx)
}

func (runtime *DiskInspectionRuntime) AuthenticateProducedTool(ctx context.Context, name string, tool *pipeline.ProducedExecutable) (pipeline.ProducedExecutableBinding, error) {
	if runtime == nil {
		return pipeline.ProducedExecutableBinding{}, e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	return runtime.container.AuthenticateProducedTool(ctx, name, tool)
}

func (runtime *DiskInspectionRuntime) InspectPackerContent(ctx context.Context, artifact *pipeline.PackerArtifact, platform string) (*DiskContentInspection, error) {
	if runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	observation, err := runtime.container.InspectPackerContent(ctx, artifact, platform)
	if err != nil {
		return nil, err
	}
	return contentFromContainerObservation(observation)
}

func (runtime *DiskInspectionRuntime) InspectPackerAcquisitionsContent(ctx context.Context, artifact *pipeline.PackerArtifact, platform string) (*DiskContentInspection, error) {
	if runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	observation, err := runtime.container.InspectPackerAcquisitionsContent(ctx, artifact, platform)
	if err != nil {
		return nil, err
	}
	return contentFromContainerObservation(observation)
}

// InspectHeldInstalledPackages accepts only held subject authority. The fixed
// container helper uses the existing exact held probe/inspector boundary; this
// owner authenticates its returned metadata against the opaque container result.
func (runtime *DiskInspectionRuntime) InspectHeldInstalledPackages(ctx context.Context, subject *heldfile.File, format, encryption string) (*DiskInstalledInventory, error) {
	if err := validateContainerInspection(ctx, format, encryption); err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	observation, err := runtime.container.InspectHeld(ctx, subject)
	if err != nil {
		return nil, err
	}
	return inventoryFromContainerObservation(observation)
}

// InspectPackerInstalledPackages owns before-copy admission, inspection and
// temporary snapshot cleanup. Only the original opaque Packer artifact enters
// this operation; neither its snapshot nor its capacity permit escapes.
func (runtime *DiskInspectionRuntime) InspectPackerInstalledPackages(ctx context.Context, artifact *pipeline.PackerArtifact, format, encryption string) (*DiskInstalledInventory, error) {
	if err := validateContainerInspection(ctx, format, encryption); err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	observation, err := runtime.container.InspectPackerArtifact(ctx, artifact)
	if err != nil {
		return nil, err
	}
	return inventoryFromContainerObservation(observation)
}

func validateContainerInspection(ctx context.Context, format, encryption string) error {
	if err := validateSBOMContext(ctx); err != nil {
		return err
	}
	if encryption != "none" {
		return e.New(e.CodeEncryptedArtifact, "disk inspection requires unencrypted content")
	}
	if format != "qcow2" {
		return e.New(e.CodeEngineCapability, "disk inspection supports only QCOW2")
	}
	return nil
}

type diskInspectionReport struct {
	Subject   string                 `json:"subject"`
	Size      int64                  `json:"size"`
	Inspector string                 `json:"inspector"`
	Codec     string                 `json:"codec"`
	Packages  []InstalledDiskPackage `json:"packages"`
}

func inventoryFromContainerObservation(observation *pipeline.DiskInspectionObservation) (*DiskInstalledInventory, error) {
	if observation == nil {
		return nil, e.New(e.CodeVerifyFailed, "disk inspection observation is absent")
	}
	raw := observation.Report()
	subject, size, image, inspector, codec := observation.Binding()
	packages, err := checkedContainerInstalledPackages(raw, subject, size, image, inspector, codec)
	if err != nil {
		return nil, err
	}
	helperName, helper := observation.ToolBinding()
	return &DiskInstalledInventory{subjectDigest: subject, subjectSize: size, inspectorDigest: inspector, codecDigest: codec, runtimeDigest: image, packages: packages, helperName: helperName, helper: helper}, nil
}

// checkedContainerInstalledPackages validates untrusted helper metadata without
// issuing authority. Only the opaque container observation above can do that.
func checkedContainerInstalledPackages(raw []byte, subject string, size int64, image, inspector, codec string) ([]InstalledDiskPackage, error) {
	if len(raw) > maxDiskPackageReportBytes {
		return nil, e.New(e.CodeEnvelopeDecode, "disk inspection report exceeds its budget")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeEnvelopeDecode, err, "validating inspection helper report")
	}
	var report diskInspectionReport
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return nil, e.Wrap(e.CodeEnvelopeDecode, err, "decoding inspection helper report")
	}
	if !canonical.IsDigest(image) || report.Subject != subject || report.Size != size || report.Inspector != inspector || report.Codec != codec || len(report.Packages) == 0 || len(report.Packages) > maxDiskInstalledPackages {
		return nil, e.New(e.CodeVerifyFailed, "inspection helper report differs from its held subject or tool binding")
	}
	// The helper parses attacker-controlled disk contents. Reapply the owning
	// metadata invariants outside the sandbox before accepting its report.
	return checkedInstalledDiskPackages(report.Packages)
}

// RunDiskInspectionHelper delegates to the isolated executable-only owner.
func RunDiskInspectionHelper(ctx context.Context, inspectorDigest, codecDigest, subjectDigest string, size int64, input io.Reader, output io.Writer) error {
	return diskinspect.RunDiskInspectionHelper(ctx, inspectorDigest, codecDigest, subjectDigest, size, input, output)
}
