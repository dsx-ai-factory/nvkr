// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"maps"
	"reflect"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Validate admits the complete Plan in dependency order. Local checks retain
// their coded diagnostics; rederivation finally checks every executable byte.
func (plan Plan) Validate() error {
	packages, err := plan.validatePlanInputs()
	if err != nil {
		return err
	}
	if err := plan.validatePlanBuilds(); err != nil {
		return err
	}
	if err := plan.validateEvidenceAttestationGraph(); err != nil {
		return err
	}
	if err := plan.validateSigningGraph(); err != nil {
		return err
	}
	if err := plan.Graph.Validate(); err != nil {
		return err
	}
	if err := plan.validateSBOMGraph(); err != nil {
		return err
	}
	if err := plan.validatePublicationEvidence(); err != nil {
		return err
	}
	builds, err := plan.validateExecutionBindings()
	if err != nil {
		return err
	}
	if err := plan.validatePlanNodes(builds); err != nil {
		return err
	}
	if err := plan.validateLifecyclePlans(); err != nil {
		return err
	}
	return plan.validateCompiledGraph(packages)
}

func (plan Plan) validatePlanInputs() ([]packageSpec, error) {
	for _, pin := range plan.Actions {
		if err := pin.Validate(); err != nil {
			return nil, err
		}
	}
	if (plan.SchemaVersion != PlanSchemaVersion && plan.SchemaVersion != CompositePlanSchemaVersion) ||
		(plan.SchemaVersion == CompositePlanSchemaVersion) != (plan.Composition != nil) || !canonicalName.MatchString(plan.Name) {
		return nil, e.New(e.CodeInvalidCatalog, "run plan schema/name is invalid")
	}
	if strings.TrimSpace(plan.GeneratorVersion) == "" {
		return nil, e.New(e.CodeInvalidCatalog, "run plan has no generator version")
	}
	if (plan.DefinitionPath == "") != (plan.EnvironmentPath == "") ||
		(plan.DefinitionPath != "" && (!safeGeneratedPath(plan.DefinitionPath) || !safeGeneratedPath(plan.EnvironmentPath))) {
		return nil, e.New(e.CodeInvalidCatalog, "run plan definition/environment source paths are incomplete or unsafe")
	}
	if !canonical.IsDigest(plan.CatalogDigest) {
		return nil, e.New(e.CodeUnpinnedSource, "run plan catalog identity is not digest-pinned")
	}
	if plan.CatalogSnapshot == nil {
		return nil, e.New(e.CodeUnverifiedRef, "run plan has no base catalog snapshot")
	}
	catalogDigest, err := plan.CatalogSnapshot.SnapshotDigest()
	if err != nil {
		return nil, err
	}
	if catalogDigest != plan.CatalogDigest {
		return nil, e.New(e.CodeUnverifiedRef, "run plan base catalog snapshot differs from catalogDigest")
	}
	if !maps.Equal(plan.Actions, plan.CatalogSnapshot.Actions) {
		return nil, e.New(e.CodePipelineDrift, "run plan action pins differ from its base catalog snapshot")
	}
	if err := admitInputs(plan.Definition, plan.Environment); err != nil {
		return nil, err
	}
	if err := admitDefinitionForm(plan.Environment, plan.Composition); err != nil {
		return nil, err
	}
	definitionDigest, err := plan.Definition.Digest()
	if err != nil {
		return nil, err
	}
	environmentDigest, err := plan.Environment.Digest()
	if err != nil {
		return nil, err
	}
	if plan.Composition != nil {
		if err := plan.Composition.Validate(plan.Definition, plan.DefinitionDigest); err != nil {
			return nil, err
		}
	} else if definitionDigest != plan.DefinitionDigest {
		return nil, e.New(e.CodeUnverifiedRef, "run plan definition does not match definitionDigest")
	}
	if plan.Name != plan.Definition.Metadata.Name {
		return nil, e.New(e.CodeUnverifiedRef, "run plan definition does not match definitionDigest")
	}
	if environmentDigest != plan.EnvironmentDigest {
		return nil, e.New(e.CodeUnverifiedRef, "run plan environment does not match environmentDigest")
	}
	if err := plan.Qualification.ValidatePlanTier(plan.Environment.Signing); err != nil {
		return nil, err
	}
	packages, targets, routes, err := derivePublication(plan.Definition, plan.Environment, plan.Builds)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(plan.Targets, targets) || !reflect.DeepEqual(plan.Routes, routes) {
		return nil, e.New(e.CodeUnverifiedRef, "run plan publication routing differs from its authored environment")
	}

	if len(plan.LifecycleOutputs) != len(plan.Definition.Outputs) {
		return nil, e.New(e.CodeUnverifiedRef, "run plan omits or adds authored output slots")
	}

	requiredBuilds := 0
	for _, output := range plan.Definition.Outputs {
		requiredBuilds += len(nativeOutputSelections(output))
	}
	if len(plan.Builds) != requiredBuilds {
		return nil, e.New(e.CodeUnverifiedRef, "run plan omits or adds authored output slots or native build uses")
	}
	for i, output := range plan.Definition.Outputs {
		bound := plan.LifecycleOutputs[i]
		if bound.Name != output.Name || bound.Kind != output.Format || !equalFactoryStrings(bound.Components, output.Components) {
			return nil, e.New(e.CodeUnverifiedRef, "run plan output %q differs from its Definition", output.Name)
		}
	}
	return packages, nil
}

func (plan Plan) validatePlanNodes(builds map[string]ir.Node) error {
	for _, node := range plan.Graph.Nodes {
		if node.HelmImage != nil {
			if node.Kind != NodePackage || node.PackageKind != PackageHelm {
				return e.New(e.CodePipelineDrift, "Helm binding on a non-Helm node")
			}
			artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
			for _, artifact := range plan.Graph.Artifacts {
				artifacts[artifact.ID] = artifact
			}
			var inputs []string
			for _, id := range node.Inputs {
				if artifacts[id].Kind != string(TierTerminalObservation) && artifacts[id].Kind != "producer-result" {
					inputs = append(inputs, id)
				}
			}
			if err := validatePackageInputs(packageSpec{Kind: PackageHelm, HelmImage: node.HelmImage}, inputs, artifacts); err != nil {
				return err
			}
		}
		if node.IRNode != "" {
			if _, err := plan.ExecutionFor(node); err != nil {
				return err
			}
		}
		if node.Kind != NodeBuild {
			continue
		}
		build, ok := builds[node.BuildTarget+"/"+node.IRNode]
		if !ok {
			return e.New(e.CodePipelineDrift, "operation %q has no selected producer instruction", node.ID)
		}
		if build.BuilderType == ir.BuilderBuildKit {
			switch plan.Environment.RuntimeBinary() {
			case "docker", "podman":
			default:
				return e.New(e.CodeEngineCapability,
					"rootless BuildKit node %q requires Docker or Podman in its Environment", node.ID)
			}
		}
		if build.BuilderType == ir.BuilderNativeGo {
			location, ok := plan.Environment.Runtime.NativeToolchains[build.Toolchain]
			if !ok || location.GoManifest == "" || plan.Environment.Publication != nil || plan.Environment.Signing != nil {
				return e.New(e.CodeEngineCapability, "native Go requires a local installed toolchain binding without publication or signing")
			}
		}
	}
	return nil
}
