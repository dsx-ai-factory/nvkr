package factory

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// DiskInstalledInventoryDescription is historical diagnostic data, not a receipt
// constructor, full disk SBOM, composition verdict, or publication authority.
type DiskInstalledInventoryDescription struct {
	OutputName      string
	Subject         DiskPayloadObservation
	RuntimeDigest   string
	InspectorDigest string
	CodecDigest     string
	Packages        []sbom.InstalledDiskPackage
}

// DiskInstalledInventoryReceipt joins an installed-package scan to the original
// live execution's material observations. Only factory can issue it. Its live
// use requires the original open owner and inspector, not equal identity fields
// or an equal-byte sibling attempt. It borrows the issuing LocalRunResult's
// lifetime: closing that Run revokes use, including shallow receipt copies.
type DiskInstalledInventoryReceipt struct {
	owner       *LocalRunResult
	runtime     *sbom.DiskInspectionRuntime
	trace       *diskMaterialTrace
	description DiskInstalledInventoryDescription
}

// Description remains readable after Close, but cannot restore live authority.
func (receipt *DiskInstalledInventoryReceipt) Description() DiskInstalledInventoryDescription {
	if receipt == nil || receipt.owner == nil || receipt.trace == nil {
		return DiskInstalledInventoryDescription{}
	}
	view := receipt.description
	view.Subject.Components = slices.Clone(view.Subject.Components)
	view.Packages = slices.Clone(view.Packages)
	return view
}

// These private seams mirror the producer boundary: controlled tests can model
// failure and close races, but callers cannot implement the production inspector.
type diskInspectionBindings struct{ image, inspector, codec string }

type diskInstalledScan struct {
	subject  string
	size     int64
	bindings diskInspectionBindings
	packages []sbom.InstalledDiskPackage
}

type diskInventoryScan func(context.Context, *diskPayload, string, string) (diskInstalledScan, error)
type diskInventoryBindings func(context.Context) (diskInspectionBindings, error)

func runtimeInventoryBindings(runtime *sbom.DiskInspectionRuntime) diskInventoryBindings {
	return func(ctx context.Context) (diskInspectionBindings, error) {
		image, inspector, codec, err := runtime.BindingContext(ctx)
		return diskInspectionBindings{image: image, inspector: inspector, codec: codec}, err
	}
}

// ObserveDiskInstalledInventory consumes the original held disk, not a path or
// decoded trace. The caller owns runtime; the Run owns the source disk. Neither
// lifetime transfers. The optional installed-inventory claim remains diagnostic.
func (owner *LocalRunResult) ObserveDiskInstalledInventory(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan, runtime *sbom.DiskInspectionRuntime,
) (*DiskInstalledInventoryReceipt, error) {
	if runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "disk installed inventory requires an admitted inspection runtime")
	}
	return owner.observeDiskInstalledInventory(ctx, plan, expected, runtime, runtimeInventoryBindings(runtime),
		packerInstalledInventoryScan(runtime.InspectPackerInstalledPackages))
}

// The concrete runtime owns the complete snapshot lifetime. This private seam
// exercises factory's handoff and result checks without admitting a substitute
// inspector through the public API.
func packerInstalledInventoryScan(inspect func(context.Context, *pipeline.PackerArtifact, string, string) (*sbom.DiskInstalledInventory, error)) diskInventoryScan {
	return func(ctx context.Context, payload *diskPayload, format, encryption string) (diskInstalledScan, error) {
		artifact, ok := payload.content.(*pipeline.PackerArtifact)
		if !ok || artifact == nil {
			return diskInstalledScan{}, e.New(e.CodeVerifyFailed, "disk inspection requires the original held Packer capability")
		}
		inventory, err := inspect(ctx, artifact, format, encryption)
		if err != nil {
			return diskInstalledScan{}, err
		}
		if inventory == nil {
			return diskInstalledScan{}, e.New(e.CodeVerifyFailed, "disk inspector returned no installed inventory")
		}
		digest, size, inspector, codec := inventory.Binding()
		return diskInstalledScan{subject: digest, size: size, packages: inventory.Packages(),
			bindings: diskInspectionBindings{image: inventory.RuntimeBinding(), inspector: inspector, codec: codec}}, nil
	}
}

func (owner *LocalRunResult) observeDiskInstalledInventory(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan, runtime *sbom.DiskInspectionRuntime,
	bindings diskInventoryBindings, scan diskInventoryScan,
) (*DiskInstalledInventoryReceipt, error) {
	if ctx == nil || owner == nil || bindings == nil || scan == nil {
		return nil, e.New(e.CodeUsage, "disk inspection requires a context, live Run and admitted inspector")
	}
	ctx, release, err := owner.beginDiskInspection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	tools, err := bindings(ctx)
	if err != nil {
		return nil, err
	}
	if !canonical.IsDigest(tools.image) || !canonical.IsDigest(tools.inspector) || !canonical.IsDigest(tools.codec) {
		return nil, e.New(e.CodeUnverifiedRef, "disk inspection runtime has incomplete exact-byte bindings")
	}
	trace, err := owner.observeDiskMaterialsWhileHeld(ctx, plan, expected)
	if err != nil {
		return nil, err
	}
	subject := trace.description.Subject
	if subject.Encryption != "none" {
		return nil, e.New(e.CodeEncryptedArtifact, "installed inventory requires an unencrypted disk")
	}
	var payload *diskPayload
	owner.diskMu.Lock()
	for _, completion := range owner.diskCompletions {
		if completion.NodeID == expected.Name+"/"+subject.Producer {
			payload = completion.Payload
		}
	}
	owner.diskMu.Unlock()
	if payload == nil {
		return nil, e.New(e.CodeVerifyFailed, "disk inspection lost its original held subject")
	}
	installed, err := scan(ctx, payload, subject.Format, subject.Encryption)
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, errors.Join(ctx.Err(), err), "disk installed inventory canceled")
	}
	if err != nil {
		return nil, err
	}
	if installed.subject != subject.Digest || installed.size != subject.Size || installed.bindings != tools || len(installed.packages) == 0 {
		return nil, e.New(e.CodeVerifyFailed, "disk installed inventory does not bind the exact subject and inspector closure")
	}
	// Recheck both owners after the child has stopped. A successful report must
	// not survive runtime/tool substitution or Run close that began mid-scan.
	current, err := bindings(ctx)
	if err != nil {
		return nil, err
	}
	if current != tools {
		return nil, e.New(e.CodeVerifyFailed, "disk inspection runtime changed during inspection")
	}
	currentTrace, err := owner.observeDiskMaterialsWhileHeld(ctx, plan, expected)
	if err != nil {
		return nil, err
	}
	if err := trace.ValidateSubject(expected.Name, currentTrace.description.Subject.PlanDigest,
		currentTrace.description.Subject.BuildDigest, currentTrace.description.Subject.Digest); err != nil {
		return nil, err
	}
	return &DiskInstalledInventoryReceipt{owner: owner, runtime: runtime, trace: trace,
		description: DiskInstalledInventoryDescription{OutputName: expected.Name, Subject: subject,
			RuntimeDigest: tools.image, InspectorDigest: tools.inspector, CodecDigest: tools.codec,
			Packages: slices.Clone(installed.packages)}}, nil
}

// ValidateDiskInstalledInventory admits live use only by the issuing owner and
// same admitted runtime. It cannot promote a serialized diagnostic description.
func (owner *LocalRunResult) ValidateDiskInstalledInventory(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan, runtime *sbom.DiskInspectionRuntime, receipt *DiskInstalledInventoryReceipt,
) error {
	if runtime == nil {
		return e.New(e.CodeEngineCapability, "validating disk inventory requires its admitted runtime")
	}
	return owner.validateDiskInstalledInventory(ctx, plan, expected, runtime, receipt, runtimeInventoryBindings(runtime))
}

func (owner *LocalRunResult) validateDiskInstalledInventory(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan, runtime *sbom.DiskInspectionRuntime, receipt *DiskInstalledInventoryReceipt,
	bindings diskInventoryBindings,
) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "validating disk inventory requires a context")
	}
	if receipt == nil || owner == nil || receipt.owner != owner || receipt.runtime != runtime || receipt.trace == nil || bindings == nil {
		return e.New(e.CodeVerifyFailed, "disk inventory belongs to a different Run or inspector")
	}
	ctx, release, err := owner.beginDiskInspection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return owner.validateDiskInstalledInventoryWhileHeld(ctx, plan, expected, runtime, receipt, bindings)
}

// The caller holds the disk permit through the complete evidence join.
func (owner *LocalRunResult) validateDiskInstalledInventoryWhileHeld(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan, runtime *sbom.DiskInspectionRuntime, receipt *DiskInstalledInventoryReceipt,
	bindings diskInventoryBindings,
) error {
	if receipt == nil || receipt.owner != owner || receipt.runtime != runtime || receipt.trace == nil || bindings == nil {
		return e.New(e.CodeVerifyFailed, "disk inventory belongs to a different Run or inspector")
	}
	trace, err := owner.observeDiskMaterialsWhileHeld(ctx, plan, expected)
	if err != nil {
		return err
	}
	subject := trace.description.Subject
	if err := receipt.trace.ValidateSubject(expected.Name, subject.PlanDigest, subject.BuildDigest, subject.Digest); err != nil {
		return err
	}
	current, err := bindings(ctx)
	if err != nil {
		return err
	}
	view := receipt.description
	if view.OutputName != expected.Name || view.Subject.Size != subject.Size ||
		current != (diskInspectionBindings{image: view.RuntimeDigest, inspector: view.InspectorDigest, codec: view.CodecDigest}) {
		return e.New(e.CodeVerifyFailed, "disk inventory subject or inspector closure changed")
	}
	return nil
}

// beginDiskInspection owns a bounded operation under the same permit used by
// disk export. Close cancels this context before waiting for its cleanup/join.
// Its ten-minute ceiling covers the whole handoff, including queueing, tracing,
// snapshotting, scanning and rechecks. The runtime's timeout is an additional
// upper bound, not a guaranteed scan allowance beyond the remaining deadline.
func (owner *LocalRunResult) beginDiskInspection(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	release, err := owner.acquireDiskPermit(ctx)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	owner.diskMu.Lock()
	if owner.diskClosed || owner.diskClosing {
		owner.diskMu.Unlock()
		cancel()
		release()
		return nil, nil, e.New(e.CodeGateUnmet, "disk inspection requires the original open Run")
	}
	owner.diskInspectionCancel = cancel
	owner.diskMu.Unlock()
	return ctx, func() {
		owner.diskMu.Lock()
		owner.diskInspectionCancel = nil
		owner.diskMu.Unlock()
		cancel()
		release()
	}, nil
}
