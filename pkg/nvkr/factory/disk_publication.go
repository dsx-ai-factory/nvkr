// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// Publication supports public-bootstrap LUKS2 only in the explicit native
// producer-provenance lane. Transport metadata cannot create this admission.
func admitDiskPublicationEncryption(plan *Plan, producer ir.Node) error {
	if err := pipeline.ValidateVMImagePackerDiskExecution(producer); err != nil {
		return err
	}
	if producer.Disk.Encryption == "luks2" && (plan == nil || !diskBuildProvenance(plan) ||
		plan.Qualification != TierPipelineAttested || plan.Environment.GitHubARC != "native-kvm-v1" || plan.Environment.Publication == nil) {
		return e.New(e.CodeEngineCapability, "public-bootstrap LUKS2 publication requires native build-provenance evidence")
	}
	return nil
}

// plannedDiskPublicationEncryption binds the declaration to this exact named
// output's frozen root before a package or restored payload is opened.
func plannedDiskPublicationEncryption(plan *Plan, expected LifecycleOutputPlan) (string, error) {
	_, artifacts := planIndex(plan)
	raw, ok := artifacts[expected.ArtifactID]
	if !ok || raw.BuildTarget != expected.Name || raw.Kind != PackageDiskImage {
		return "", e.New(e.CodePipelineDrift, "disk publication lacks its exact planned raw root")
	}
	build, producer, err := plan.producerInstruction(planNode(plan, raw.Producer))
	if err != nil {
		return "", err
	}
	if build.Name != expected.Name || build.BuildDigest != expected.BuildDigest || producer.Component != raw.Component {
		return "", e.New(e.CodePipelineDrift, "disk publication root differs from its named Build identity")
	}
	if err := admitDiskPublicationEncryption(plan, producer); err != nil {
		return "", err
	}
	return producer.Disk.Encryption, nil
}

func diskPublicationWrapperID(output LifecycleOutputPlan) string {
	return "package/" + output.Name
}

func diskPublicationProducerResultID(output LifecycleOutputPlan, raw Artifact) string {
	return "result/" + output.Name + "/" + raw.Component
}

// inspectFactoryDisk checks the exact wrapper profile and bytes. This is a
// transport observation, not reconstruction of the live evidence receipt.
func inspectFactoryDisk(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, artifact Artifact) (delivery.DiskArtifactInspection, error) {
	if expected.Kind != PackageDiskImage || artifact.ID != diskPublicationWrapperID(expected) || artifact.Kind != PackageDiskImage ||
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
	encryption, err := plannedDiskPublicationEncryption(plan, expected)
	if err != nil {
		return delivery.DiskArtifactInspection{}, err
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
	if inspection.ArtifactType != artifact.MediaType || inspection.Encryption != encryption {
		return delivery.DiskArtifactInspection{}, e.New(e.CodeVerifyFailed, "disk wrapper differs from planned artifact type or encryption")
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
	wrapper, wrapperFound := artifacts[diskPublicationWrapperID(expected)]
	wantedRaw, wantedWrapper := planned[expected.ArtifactID], planned[diskPublicationWrapperID(expected)]
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
	record, found := artifacts[diskPublicationProducerResultID(expected, raw)]
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
			Component: raw.Component, Format: "qcow2", Encryption: inspection.Encryption, Size: inspection.Size,
			PlanDigest: planDigest, Outcome: "succeeded", EvidenceMode: plan.Environment.DiskEvidence}}
	if err := output.Validate(); err != nil {
		return delivery.Output{}, err
	}
	return output, nil
}
