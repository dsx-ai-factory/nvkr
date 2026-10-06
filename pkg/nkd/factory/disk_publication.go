package factory

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// inspectFactoryDisk checks the exact wrapper profile and bytes. This is a
// transport observation, not reconstruction of the live evidence receipt.
func inspectFactoryDisk(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, artifact Artifact) (delivery.DiskArtifactInspection, error) {
	if expected.Kind != PackageDiskImage || artifact.ID != "package/"+expected.Name || artifact.Kind != PackageDiskImage ||
		artifact.Name != expected.Name || artifact.BuildTarget != "" || artifact.MediaType != packageMediaType(PackageDiskImage) {
		return delivery.DiskArtifactInspection{}, e.New(e.CodeVerifyFailed, "disk publication substitutes its exact planned wrapper")
	}
	platform := ""
	for _, build := range plan.Builds {
		if build.Name == expected.Name && build.BuildDigest == expected.BuildDigest {
			platform = build.Platform
		}
	}
	if platform == "" {
		return delivery.DiskArtifactInspection{}, e.New(e.CodePipelineDrift, "disk wrapper has no exact planned build")
	}
	held, err := delivery.OpenSingleDiskArtifact(ctx, artifact.Path, platform, 64<<30)
	if err != nil {
		return delivery.DiskArtifactInspection{}, err
	}
	inspection := held.Inspection()
	profileErr := held.RequireStandardProfile(ctx)
	if err := errors.Join(profileErr, held.Close()); err != nil {
		return delivery.DiskArtifactInspection{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "checking standard disk wrapper"))
	}
	if inspection.ArtifactType != artifact.MediaType || inspection.Encryption != "none" {
		return delivery.DiskArtifactInspection{}, e.New(e.CodeVerifyFailed, "disk wrapper differs from planned artifact type or plain profile")
	}
	return inspection, nil
}

// diskPublicationRequirement selects only the compiler's raw root artifact.
// Its restored carrier is the separately named standard wrapper, not a second
// raw QCOW2 file and not a serialized capability.
func diskPublicationRequirement(plan *Plan, artifact Artifact) (LifecycleOutputPlan, bool) {
	if plan == nil || plan.Environment.Publication == nil || artifact.Kind != PackageDiskImage || artifact.BuildTarget == "" {
		return LifecycleOutputPlan{}, false
	}
	_, planned := planIndex(plan)
	for _, expected := range plan.LifecycleOutputs {
		if expected.Kind == PackageDiskImage && expected.Name == artifact.BuildTarget && expected.ArtifactID == artifact.ID &&
			plan.hasCompiledDiskTarget(expected.Name, planned) {
			return expected, true
		}
	}
	return LifecycleOutputPlan{}, false
}

// ObserveDiskPublicationPayload joins a restored standard wrapper with the
// compiler's raw subject and exact producer record. It issues no receipt or
// signing authority: the hosted finalizer must still validate complete evidence,
// publication membership and the existing signed provenance closure.
func ObserveDiskPublicationPayload(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, artifacts map[string]Artifact) (delivery.Output, error) {
	if ctx == nil || plan == nil {
		return delivery.Output{}, e.New(e.CodeUsage, "disk publication observation requires context and Plan")
	}
	if err := ctx.Err(); err != nil {
		return delivery.Output{}, e.Wrap(e.CodeTimeout, err, "disk publication observation canceled")
	}
	if err := plan.Validate(); err != nil {
		return delivery.Output{}, err
	}
	_, planned := planIndex(plan)
	matched := false
	for _, binding := range plan.LifecycleOutputs {
		if binding.Name == expected.Name {
			matched = binding.Kind == expected.Kind && binding.ArtifactID == expected.ArtifactID &&
				binding.BuildDigest == expected.BuildDigest && slices.Equal(binding.Components, expected.Components)
		}
	}
	if !matched {
		return delivery.Output{}, e.New(e.CodePipelineDrift, "disk publication changes its frozen raw lifecycle binding")
	}
	if !plan.hasCompiledDiskTarget(expected.Name, planned) || plan.Environment.Publication == nil {
		return delivery.Output{}, e.New(e.CodeEngineCapability, "disk publication requires its compiled production wrapper graph")
	}
	raw, rawFound := artifacts[expected.ArtifactID]
	wrapper, wrapperFound := artifacts["package/"+expected.Name]
	wantedRaw, wantedWrapper := planned[expected.ArtifactID], planned["package/"+expected.Name]
	if !rawFound || !wrapperFound || raw.Kind != PackageDiskImage || raw.BuildTarget != expected.Name || raw.Component == "" ||
		raw.ID != wantedRaw.ID || raw.Producer != wantedRaw.Producer || raw.Component != wantedRaw.Component ||
		wrapper.ID != wantedWrapper.ID || wrapper.Producer != wantedWrapper.Producer || wrapper.Name != wantedWrapper.Name ||
		wrapper.Kind != wantedWrapper.Kind || wrapper.MediaType != wantedWrapper.MediaType {
		return delivery.Output{}, e.New(e.CodeReleaseIncomplete, "disk publication lacks its raw subject or wrapper")
	}
	inspection, err := inspectFactoryDisk(ctx, plan, expected, wrapper)
	if err != nil {
		return delivery.Output{}, err
	}
	record, found := artifacts["result/"+expected.Name+"/"+raw.Component]
	if !found || record.Kind != "producer-result" || record.Producer != raw.Producer || record.Component != raw.Component || record.BuildTarget != expected.Name {
		return delivery.Output{}, e.New(e.CodeProvenanceMissing, "disk wrapper lacks its exact raw producer record")
	}
	result, err := readProducerResult(ctx, record.Path)
	if err != nil {
		return delivery.Output{}, err
	}
	if result.Component != raw.Component || result.OutputType != PackageDiskImage || result.SHA256 != inspection.PayloadDigest || result.Size != inspection.Size {
		return delivery.Output{}, e.New(e.CodeVerifyFailed, "disk wrapper substitutes the exact raw producer subject")
	}
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		return delivery.Output{}, err
	}
	output := delivery.Output{Name: expected.Name, Kind: PackageDiskImage, Digest: inspection.ManifestDigest,
		BuildDigest: expected.BuildDigest, Components: append([]string(nil), expected.Components...),
		Disk: &delivery.DiskOutput{PayloadDigest: inspection.PayloadDigest, Producer: strings.TrimPrefix(raw.Producer, expected.Name+"/"),
			Component: raw.Component, Format: "qcow2", Encryption: "none", Size: inspection.Size,
			PlanDigest: planDigest, Outcome: "succeeded"}}
	if err := output.Validate(); err != nil {
		return delivery.Output{}, err
	}
	return output, nil
}
