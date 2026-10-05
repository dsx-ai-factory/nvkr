package factory

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// DiskEvidenceReceipt borrows its original Run and runtime. Only a successful
// live evidence join can issue it; equal-byte sibling attempts, decoded documents
// and descriptive accessors cannot restore publication authority.
type DiskEvidenceReceipt struct {
	owner     *LocalRunResult
	runtime   *sbom.DiskInspectionRuntime
	trace     *diskMaterialTrace
	profile   definition.DiskInspectionProfile
	root      []byte
	documents map[string][]byte
}

func (receipt *DiskEvidenceReceipt) Subject() (string, int64) {
	if receipt == nil || receipt.owner == nil || receipt.trace == nil {
		return "", 0
	}
	return receipt.trace.description.Subject.Digest, receipt.trace.description.Subject.Size
}

// ComponentDocuments returns independent descriptions, not restored authority.
// Publication must first authenticate this receipt through its original owner.
func (receipt *DiskEvidenceReceipt) ComponentDocuments() (map[string]*sbom.BOM, error) {
	if receipt == nil || receipt.owner == nil {
		return nil, e.New(e.CodeProvenanceMissing, "component documents require an original receipt owner")
	}
	result := make(map[string]*sbom.BOM, len(receipt.documents))
	for _, name := range slices.Sorted(maps.Keys(receipt.documents)) {
		raw := receipt.documents[name]
		var bom sbom.BOM
		if err := canonical.UnmarshalExact(raw, &bom); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding component document %q", name)
		}
		if err := bom.Validate(); err != nil {
			return nil, err
		}
		result[name] = &bom
	}
	return result, nil
}

// OpenDiskEvidenceRuntime selects observation infrastructure from the frozen
// Environment. Native host tooling is independent of the image's consumer arch.
func OpenDiskEvidenceRuntime(ctx context.Context, plan *Plan, runtimeExecutable string, workspace *os.File) (*sbom.DiskInspectionRuntime, error) {
	if ctx == nil || plan == nil {
		return nil, e.New(e.CodeUsage, "disk evidence admission requires context and Plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if filepath.Base(runtimeExecutable) != plan.Environment.RuntimeBinary() {
		return nil, e.New(e.CodeEngineCapability, "disk inspector runtime differs from frozen Environment runtime")
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	profile, ok := plan.Environment.DiskInspectionProfiles[platform]
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "frozen Environment has no native disk inspection profile")
	}
	if err := profile.Validate(platform); err != nil {
		return nil, err
	}
	if err := requireProductionDiskScratch(profile); err != nil {
		return nil, err
	}
	if err := validateDiskScannerSelection(plan, platform, profile.ScannerDigest); err != nil {
		return nil, err
	}
	return sbom.OpenDiskInspectionRuntime(ctx, pipeline.DiskInspectionSelection{RuntimeExecutable: runtimeExecutable, Workspace: workspace,
		PodmanStorageDriver: plan.Environment.Runtime.PodmanStorageDriver,
		Image:               profile.Image, Platform: platform, InspectorDigest: profile.InspectorDigest, CodecDigest: profile.CodecDigest,
		GuestfishDigest: profile.GuestfishDigest, ScannerDigest: profile.ScannerDigest, PackageReaderDigest: profile.PackageReaderDigest,
		Resources: profile.Resources, TimeoutSeconds: profile.TimeoutSeconds, MaxFilesystemBytes: profile.MaxFilesystemBytes, ScratchStorage: profile.ScratchStorage})
}

func diskEvidenceProfile(ctx context.Context, plan *Plan, inspection *sbom.DiskInspectionRuntime) (definition.DiskInspectionProfile, error) {
	if plan == nil || inspection == nil {
		return definition.DiskInspectionProfile{}, e.New(e.CodeEngineCapability, "disk evidence requires its frozen Plan and admitted inspector")
	}
	selection, err := inspection.SelectionContext(ctx)
	if err != nil {
		return definition.DiskInspectionProfile{}, err
	}
	profile, ok := plan.Environment.DiskInspectionProfiles[selection.Platform]
	if !ok {
		return definition.DiskInspectionProfile{}, e.New(e.CodeEngineCapability, "disk evidence profile is not frozen into the Plan")
	}
	if err := profile.Validate(selection.Platform); err != nil {
		return definition.DiskInspectionProfile{}, err
	}
	if err := requireProductionDiskScratch(profile); err != nil {
		return definition.DiskInspectionProfile{}, err
	}
	if err := validateDiskScannerSelection(plan, selection.Platform, profile.ScannerDigest); err != nil {
		return definition.DiskInspectionProfile{}, err
	}
	if selection.Image != profile.Image || selection.InspectorDigest != profile.InspectorDigest || selection.CodecDigest != profile.CodecDigest ||
		filepath.Base(selection.RuntimeExecutable) != plan.Environment.RuntimeBinary() ||
		selection.PodmanStorageDriver != plan.Environment.Runtime.PodmanStorageDriver ||
		selection.GuestfishDigest != profile.GuestfishDigest || selection.ScannerDigest != profile.ScannerDigest || selection.PackageReaderDigest != profile.PackageReaderDigest ||
		selection.Resources != profile.Resources || selection.TimeoutSeconds != profile.TimeoutSeconds || selection.MaxFilesystemBytes != profile.MaxFilesystemBytes || selection.ScratchStorage != profile.ScratchStorage {
		return definition.DiskInspectionProfile{}, e.New(e.CodeVerifyFailed, "admitted disk inspector differs from frozen Environment profile")
	}
	return profile, nil
}

// A frozen inspection profile owns its complete scanner pin. Reuse a separately
// selected Definition scanner when it targets this native host. A profile for
// the other supported host is explicit observation infrastructure, not an
// invented platform substitution for a release asset compiled for another ISA.
func validateDiskScannerSelection(plan *Plan, platform, pin string) error {
	if plan.CatalogSnapshot == nil {
		return e.New(e.CodeInvalidCatalog, "disk scanner selection requires its frozen catalog")
	}
	if plan.Definition.SBOMTool == "" {
		return nil
	}
	selected, ok := plan.CatalogSnapshot.Tools[plan.Definition.SBOMTool]
	if !ok {
		return e.New(e.CodeUnpinnedInput, "disk scanner selection requires its selected catalog tool")
	}
	if selected.Platform == platform && selected.ExecutableDigest != pin {
		return e.New(e.CodeVerifyFailed, "disk profile substitutes the selected native Definition scanner")
	}
	return nil
}

// ReconcileDiskEvidence scans the original disk, its original retained guest
// acquisitions, and the complete live staged producer graph. No caller path,
// input description or completeness boolean is admitted.
func (owner *LocalRunResult) ReconcileDiskEvidence(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, inspection *sbom.DiskInspectionRuntime) (*DiskEvidenceReceipt, error) {
	if ctx == nil || owner == nil {
		return nil, e.New(e.CodeUsage, "disk reconciliation requires context and original Run")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "disk reconciliation canceled before admission")
	}
	build, err := admitDiskEvidenceBuild(plan, expected)
	if err != nil {
		return nil, err
	}
	ctx, release, err := owner.beginDiskInspection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	profile, err := diskEvidenceProfile(ctx, plan, inspection)
	if err != nil {
		return nil, err
	}
	trace, err := owner.observeDiskMaterialsWhileHeld(ctx, plan, expected)
	if err != nil {
		return nil, err
	}
	subject := trace.description.Subject
	if subject.Encryption != "none" {
		return nil, e.New(e.CodeEncryptedArtifact, "disk evidence requires unencrypted inspected contents")
	}
	owner.diskMu.Lock()
	var artifact *pipeline.PackerArtifact
	for _, completion := range owner.diskCompletions {
		if completion.NodeID == expected.Name+"/"+subject.Producer && completion.Payload != nil {
			artifact, _ = completion.Payload.content.(*pipeline.PackerArtifact)
		}
	}
	owner.diskMu.Unlock()
	if artifact == nil {
		return nil, e.New(e.CodeVerifyFailed, "disk evidence lost its original held Packer artifact")
	}
	// Reject absent, changed or wrong-architecture capture before expensive
	// filesystem discovery. This comes only from the original artifact owner.
	acquisitions, err := artifact.ObserveAcquisitionsContext(ctx)
	if err != nil {
		return nil, err
	}
	if acquisitions.Description().Architecture != strings.TrimPrefix(build.Platform, "linux/") {
		return nil, e.New(e.CodeVerifyFailed, "retained acquisitions differ from the disk consumer architecture")
	}
	root, err := inspectOriginalDiskContent(ctx, inspection, artifact, subject.Size, build.Platform)
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]string, len(trace.description.Members))
	for _, member := range trace.description.Members {
		hashes[member.Component] = member.Digest
	}
	sources, _, err := sbom.PerArtifact(&buildgraph.Resolved{Platform: build.Platform, Components: build.ResolvedComponents}, build.BuildDigest, hashes)
	if err != nil {
		return nil, err
	}
	inputs, documents, err := owner.inspectDiskEvidenceMaterials(ctx, plan, build, trace, inspection, sources)
	if err != nil {
		return nil, err
	}
	captured, err := inspection.InspectPackerAcquisitionsContent(ctx, artifact, build.Platform)
	if err != nil {
		return nil, err
	}
	inputs = append(inputs, captured)
	bom, err := sbom.ReconcileDiskContents(ctx, subject.BuildDigest, subject.Component, root, inputs)
	if err != nil {
		return nil, err
	}
	bom, err = sbom.WithSourceSubject(bom, sources[subject.Component])
	if err != nil {
		return nil, err
	}
	raw, err := bom.Marshal()
	if err != nil {
		return nil, err
	}
	documents[subject.Component] = raw
	current, err := diskEvidenceProfile(ctx, plan, inspection)
	if err != nil {
		return nil, err
	}
	if current != profile {
		return nil, e.New(e.CodeVerifyFailed, "disk inspection profile changed during evidence join")
	}
	if _, err := owner.observeDiskMaterialsWhileHeld(ctx, plan, expected); err != nil {
		return nil, err
	}
	return &DiskEvidenceReceipt{owner: owner, runtime: inspection, trace: trace, profile: profile, root: raw, documents: documents}, nil
}

func requireProductionDiskScratch(profile definition.DiskInspectionProfile) error {
	if profile.ScratchStorage != "private-volume" {
		return e.New(e.CodeEngineCapability, "production disk evidence requires private disk-backed scratch")
	}
	return nil
}

// Admit Plan-decidable requirements before the Run permit, snapshots or scans.
// Whether raw material bytes are a supported ISO is still checked by guestfish;
// output kind alone grants no filesystem or installed-content proof.
func admitDiskEvidenceBuild(plan *Plan, expected LifecycleOutputPlan) (BuildPlan, error) {
	if plan == nil {
		return BuildPlan{}, e.New(e.CodeUsage, "disk evidence requires its frozen Plan")
	}
	if err := plan.Validate(); err != nil {
		return BuildPlan{}, err
	}
	for _, build := range plan.Builds {
		if build.Name != expected.Name || build.BuildDigest != expected.BuildDigest {
			continue
		}
		if build.Platform != "linux/amd64" && build.Platform != "linux/arm64" {
			return BuildPlan{}, e.New(e.CodeEngineCapability, "disk reconciliation requires an AMD64 or ARM64 consumer")
		}
		root, err := buildgraph.DiskRootName(build.ResolvedComponents)
		if err != nil {
			return BuildPlan{}, err
		}
		for _, node := range build.Graph.Nodes {
			if node.Kind != ir.KindBuild && node.Kind != ir.KindPull {
				continue
			}
			if node.Component == root {
				if node.Packer == nil || node.Packer.AcquisitionArchive == "" {
					return BuildPlan{}, e.New(e.CodeEngineCapability, "disk evidence requires a declared same-attempt acquisition carrier")
				}
				continue
			}
			switch node.OutputType {
			case "raw", "host-package", "tarball", "oci-image":
			default:
				return BuildPlan{}, e.New(e.CodeEngineCapability, "disk material kind %q has no admitted content scanner", node.OutputType)
			}
		}
		return build, nil
	}
	return BuildPlan{}, e.New(e.CodePipelineDrift, "disk evidence has no exact planned build")
}

func inspectOriginalDiskContent(ctx context.Context, inspection *sbom.DiskInspectionRuntime, artifact *pipeline.PackerArtifact, size int64, platform string) (*sbom.DiskContentInspection, error) {
	if artifact.Size() != size {
		return nil, e.New(e.CodeVerifyFailed, "original disk size changed before content inspection")
	}
	return inspection.InspectPackerContent(ctx, artifact, platform)
}

func (owner *LocalRunResult) inspectDiskEvidenceMaterials(ctx context.Context, plan *Plan, build BuildPlan, trace *diskMaterialTrace, inspection *sbom.DiskInspectionRuntime, sources map[string]*sbom.BOM) ([]*sbom.DiskContentInspection, map[string][]byte, error) {
	artifacts := make(map[string]Artifact)
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	nodes := make(map[string]ir.Node)
	for _, node := range build.Graph.Nodes {
		nodes[node.ID] = node
	}
	var inputs []*sbom.DiskContentInspection
	documents := make(map[string][]byte)
	for _, member := range trace.description.Members {
		if member.Producer == trace.description.Subject.Producer {
			continue
		}
		node := nodes[member.Producer]
		payload, ok := artifacts["artifact/"+build.Name+"/"+member.Component]
		record, recordOK := artifacts["result/"+build.Name+"/"+member.Component]
		if !ok || !recordOK || payload.Producer != build.Name+"/"+member.Producer || record.Producer != payload.Producer {
			return nil, nil, e.New(e.CodePipelineDrift, "disk material has no exact graph payload/result edge")
		}
		observed, err := pipeline.AuthenticateResultForConsumption(ctx, node, payload.Path, record.Path)
		if err != nil {
			return nil, nil, err
		}
		if observed.SHA256 != member.Digest || observed.Size != member.Size || observed.AbsPath == "" || observed.TransportPath != "" {
			return nil, nil, e.New(e.CodeVerifyFailed, "disk material differs from retained producer consumption")
		}
		kind := member.Kind
		if kind == "raw" {
			kind = "installer-iso"
		}
		if kind != "installer-iso" && kind != "host-package" && kind != "tarball" && kind != "oci-image" {
			return nil, nil, e.New(e.CodeEngineCapability, "disk material kind %q has no admitted content scanner", member.Kind)
		}
		held, err := heldfile.OpenContext(ctx, observed.AbsPath, heldfile.Options{Label: "live-disk-material", MaxBytes: member.Size, ExpectedDigest: member.Digest})
		if err != nil {
			return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "holding exact live disk material")
		}
		content, scanErr := inspection.InspectHeldContent(ctx, held, kind, build.Platform)
		closeErr := held.Close()
		if err := errors.Join(scanErr, closeErr); err != nil {
			return nil, nil, err
		}
		inputs = append(inputs, content)
		bom, err := sbom.DiskContentDocument(build.BuildDigest, member.Component, content)
		if err != nil {
			return nil, nil, err
		}
		bom, err = sbom.WithSourceSubject(bom, sources[member.Component])
		if err != nil {
			return nil, nil, err
		}
		raw, err := bom.Marshal()
		if err != nil {
			return nil, nil, err
		}
		documents[member.Component] = raw
	}
	return inputs, documents, nil
}

// ExportReconciledDisk holds the same Run lifetime permit through validation,
// exact raw export and document cloning. Closing either owner revokes live use.
// Caller-owned staging bytes may remain on failure and convey no authority;
// publication must discard that rejected staging rather than commit it.
func (owner *LocalRunResult) ExportReconciledDisk(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, inspection *sbom.DiskInspectionRuntime, receipt *DiskEvidenceReceipt, destination io.Writer) (DiskPayloadObservation, *sbom.BOM, error) {
	return owner.exportReconciledDisk(ctx, plan, expected, inspection, receipt, destination,
		func(ctx context.Context) (definition.DiskInspectionProfile, error) {
			return diskEvidenceProfile(ctx, plan, inspection)
		})
}

// The private seam models runtime close/substitution during streaming. Public
// callers always use the concrete inspector above, never supplied bindings.
func (owner *LocalRunResult) exportReconciledDisk(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, inspection *sbom.DiskInspectionRuntime, receipt *DiskEvidenceReceipt, destination io.Writer, bindings func(context.Context) (definition.DiskInspectionProfile, error)) (DiskPayloadObservation, *sbom.BOM, error) {
	if ctx == nil || destination == nil || bindings == nil {
		return DiskPayloadObservation{}, nil, e.New(e.CodeUsage, "reconciled export requires context and staging writer")
	}
	if owner == nil || receipt == nil || receipt.owner != owner || receipt.runtime != inspection || receipt.trace == nil || len(receipt.root) == 0 {
		return DiskPayloadObservation{}, nil, e.New(e.CodeVerifyFailed, "disk evidence belongs to another Run or inspector")
	}
	ctx, release, err := owner.beginDiskInspection(ctx)
	if err != nil {
		return DiskPayloadObservation{}, nil, err
	}
	defer release()
	profile, err := bindings(ctx)
	if err != nil {
		return DiskPayloadObservation{}, nil, err
	}
	if profile != receipt.profile {
		return DiskPayloadObservation{}, nil, e.New(e.CodeVerifyFailed, "reconciled inspector profile was substituted")
	}
	trace, err := owner.observeDiskMaterialsWhileHeld(ctx, plan, expected)
	if err != nil {
		return DiskPayloadObservation{}, nil, err
	}
	subject := trace.description.Subject
	if err := receipt.trace.ValidateSubject(expected.Name, subject.PlanDigest, subject.BuildDigest, subject.Digest); err != nil {
		return DiskPayloadObservation{}, nil, err
	}
	if subject.Size != receipt.trace.description.Subject.Size {
		return DiskPayloadObservation{}, nil, e.New(e.CodeVerifyFailed, "reconciled disk size changed")
	}
	owner.diskMu.Lock()
	execution := diskExecutionState{PlanDigest: owner.PlanDigest, Nodes: append([]LocalNodeResult(nil), owner.Nodes...)}
	completions := append([]diskProducerCompletion(nil), owner.diskCompletions...)
	owner.diskMu.Unlock()
	observation, _, err := inspectDiskLifecyclePayload(ctx, plan, expected, execution, completions, destination, true)
	if err != nil {
		return DiskPayloadObservation{}, nil, err
	}
	var bom sbom.BOM
	if err := canonical.UnmarshalExact(receipt.root, &bom); err != nil {
		return DiskPayloadObservation{}, nil, e.Wrap(e.CodeVerifyFailed, err, "restoring issued disk evidence bytes")
	}
	if err := bom.Validate(); err != nil {
		return DiskPayloadObservation{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return DiskPayloadObservation{}, nil, e.Wrap(e.CodeTimeout, err, "reconciled disk export canceled")
	}
	current, err := bindings(ctx)
	if err != nil {
		return DiskPayloadObservation{}, nil, err
	}
	if current != receipt.profile {
		return DiskPayloadObservation{}, nil, e.New(e.CodeVerifyFailed, "reconciled inspector profile changed during export")
	}
	return observation, &bom, nil
}
