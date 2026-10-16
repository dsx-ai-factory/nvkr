// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const (
	ArtifactPreparedEvidenceMetadata = "prepared-evidence-metadata"
	ArtifactAttestedEvidenceMetadata = "attested-evidence-metadata"
)

func preparedEvidenceMetadataID(route string) string { return "prepared-controls/" + route }
func attestedEvidenceMetadataID(route string) string { return "attested-controls/" + route }

// preparedEvidenceRoute selects the one explicit metadata-only evidence route.
// Aggregate and legacy publication retain their separately admitted dataflow.
func preparedEvidenceRoute(plan *Plan, node Node) (Route, bool) {
	if !PerOutputPublication(plan) || plan.Environment.Signing == nil || plan.Environment.Signing.Mode != SigningModeCosign ||
		(node.Kind != NodePublish && node.Kind != NodeAttestEvidence) {
		return Route{}, false
	}
	for _, route := range plan.Routes {
		if route.Name != "release" && node.ID == node.Kind+"/"+route.Name && node.Target == route.Target {
			return route, true
		}
	}
	return Route{}, false
}

func bindMetadataEvidenceAttestation(plan *Plan, route Route, index int) error {
	payload := plan.Graph.Artifacts[index]
	payload.Path = filepath.ToSlash(filepath.Join(".nvkr", "factory", "prepared", route.Name))
	prepared := Artifact{ID: preparedEvidenceMetadataID(route.Name), Name: route.Name, Kind: ArtifactPreparedEvidenceMetadata,
		Producer: payload.Producer, Target: route.Target, Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", "prepared-controls", route.Name))}
	attested := Artifact{ID: attestedEvidenceMetadataID(route.Name), Name: route.Name, Kind: ArtifactAttestedEvidenceMetadata,
		Producer: NodeAttestEvidence + "/" + route.Name, Target: route.Target, Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", "attested-controls", route.Name))}
	tool := "tool/" + plan.Definition.CosignTool
	stage := Node{ID: attested.Producer, Kind: NodeAttestEvidence, Target: route.Target,
		Inputs: uniqueSorted([]string{prepared.ID, tool}), Outputs: []string{attested.ID}, Network: ir.NetworkDefault}
	producerFound, publisherFound := false, false
	for i, node := range plan.Graph.Nodes {
		if node.ID == payload.Producer {
			if node.Kind != NodePackage || node.PackageKind != PackageOCI || len(node.Outputs) != 1 || node.Outputs[0] != payload.ID {
				return e.New(e.CodePipelineDrift, "metadata SBOM preparation requires its exact package producer")
			}
			plan.Graph.Nodes[i].Outputs = uniqueSorted([]string{payload.ID, prepared.ID})
			stage.RunsOn = node.RunsOn
			producerFound = true
		}
		if node.ID == NodePublish+"/"+route.Name {
			if node.Kind != NodePublish || node.Target != route.Target || !slices.Equal(node.Inputs, []string{payload.ID}) {
				return e.New(e.CodePipelineDrift, "metadata SBOM completion requires its exact publisher")
			}
			plan.Graph.Nodes[i].Inputs = uniqueSorted([]string{payload.ID, attested.ID, tool})
			publisherFound = true
		}
	}
	if !producerFound || !publisherFound {
		return e.New(e.CodePipelineDrift, "metadata SBOM route has no preparation or publication node")
	}
	plan.Graph.Artifacts[index] = payload
	plan.Graph.Artifacts = append(plan.Graph.Artifacts, prepared, attested)
	plan.Graph.Nodes = append(plan.Graph.Nodes, stage)
	return nil
}

// admitEvidenceMetadata checks descriptive controls against the exact Plan and
// route before signing. It confers no payload authority: publication must rejoin
// the original bytes and cryptographically verify the completed evidence.
func admitEvidenceMetadata(plan *Plan, route Route, inspection delivery.Inspection) error {
	digest, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	metadata := inspection.Root.Release
	if metadata == nil || metadata.PlanDigest != digest || metadata.DefinitionDigest != plan.DefinitionDigest || metadata.EnvironmentDigest != plan.EnvironmentDigest {
		return e.New(e.CodeVerifyFailed, "SBOM controls differ from the exact preparation Plan")
	}
	run, err := publicationRunContext(plan, digest, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return err
	}
	if err := admitPlanReferenceInvocation(plan, metadata.InvocationID, run.InvocationID()); err != nil {
		return err
	}
	var required []LifecycleOutputPlan
	for _, output := range plan.LifecycleOutputs {
		if outputPublicationRoute(output.Name) == route.Name {
			required = append(required, output)
		}
	}
	if len(required) != 1 {
		return e.New(e.CodePipelineDrift, "SBOM controls require one exact output route")
	}
	for _, object := range inspection.Root.Objects {
		if object.Output != nil && object.Output.Publication != nil {
			return e.New(e.CodeVerifyFailed, "output-local SBOM controls contain a foreign publication origin")
		}
	}
	outputs, err := delivery.MetadataOutputs(inspection.Root)
	if err != nil {
		return err
	}
	return ValidateLifecycleOutputs(required, outputs)
}

func executeAttestEvidenceMetadata(ctx context.Context, plan *Plan, node Node, route Route, artifacts map[string]Artifact, newSigner signerFor, stdout io.Writer) error {
	if _, err := runtimeTarget(plan, route.Target); err != nil {
		return err
	}
	input, err := signNodeInput(node, artifacts, ArtifactPreparedEvidenceMetadata)
	if err != nil {
		return err
	}
	output, err := signNodeOutput(node, artifacts, ArtifactAttestedEvidenceMetadata)
	if err != nil {
		return err
	}
	inspection, err := delivery.InspectMetadata(ctx, input.Path)
	if err != nil {
		return err
	}
	if err := admitEvidenceMetadata(plan, route, inspection); err != nil {
		return err
	}
	signer, err := newSigner(ctx, plan, node, artifacts)
	if err != nil {
		return err
	}
	attester, ok := signer.(sign.SBOMAttester)
	if !ok {
		return e.New(e.CodeEngineCapability, "signer has no SBOM attestation capability")
	}
	result, err := delivery.AttestPreparedMetadata(ctx, input.Path, output.Path, inspection.RootDigest, attester.AttestSBOM)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Created subject-bound SBOM attestation controls: %s\n", result.RootDigest)
	return nil
}

func validateMetadataEvidenceGraph(plan *Plan, route Route, nodes map[string]Node, artifacts map[string]Artifact) error {
	payload := artifacts[route.Input]
	prepared := artifacts[preparedEvidenceMetadataID(route.Name)]
	attested := artifacts[attestedEvidenceMetadataID(route.Name)]
	producer := nodes[payload.Producer]
	stage := nodes[NodeAttestEvidence+"/"+route.Name]
	publisher := nodes[NodePublish+"/"+route.Name]
	tool := "tool/" + plan.Definition.CosignTool
	if payload.ID != route.Input || payload.Kind != PackageOCI || producer.Kind != NodePackage || producer.PackageKind != PackageOCI ||
		prepared.Kind != ArtifactPreparedEvidenceMetadata || prepared.Producer != producer.ID ||
		attested.Kind != ArtifactAttestedEvidenceMetadata || attested.Producer != stage.ID ||
		stage.Kind != NodeAttestEvidence || stage.Target != route.Target || publisher.Kind != NodePublish || publisher.Target != route.Target ||
		!slices.Equal(producer.Outputs, uniqueSorted([]string{payload.ID, prepared.ID})) ||
		!slices.Equal(stage.Inputs, uniqueSorted([]string{prepared.ID, tool})) || !slices.Equal(stage.Outputs, []string{attested.ID}) ||
		!slices.Equal(publisher.Inputs, uniqueSorted([]string{payload.ID, attested.ID, tool})) {
		return e.New(e.CodePipelineDrift, "production route must join original payload with independently attested SBOM controls")
	}
	return nil
}
