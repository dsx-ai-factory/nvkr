// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Compiled description only; actual use needs the original Run and opaque
// producer/content owners. Native inspection cannot borrow a foreign ISA pin.
func debianInspectionProfile(build BuildPlan, environment definition.Environment, scanner catalog.ToolPin) (definition.DiskInspectionProfile, error) {
	if build.Debian == nil || build.Platform != build.Debian.Platform || build.Platform != environment.UtilityPlatform {
		return definition.DiskInspectionProfile{}, e.New(e.CodeEngineCapability, "Debian inspection requires its exact native producer/utility platform")
	}
	profile, ok := environment.DiskInspectionProfiles[build.Platform]
	if !ok {
		return profile, e.New(e.CodeEngineCapability, "Debian inspection profile is absent")
	}
	if err := profile.Validate(build.Platform); err != nil {
		return profile, err
	}
	if err := requireProductionDiskScratch(profile); err != nil {
		return profile, err
	}
	if scanner.Platform != build.Platform || scanner.ExecutableDigest != profile.ScannerDigest {
		return profile, e.New(e.CodeVerifyFailed, "Debian profile changes the selected native catalog scanner")
	}
	return profile, nil
}
func debianInspectionBuild(plan *Plan, name string) (BuildPlan, bool) {
	for _, build := range plan.Builds {
		if build.Name == name && build.Debian != nil {
			return build, true
		}
	}
	return BuildPlan{}, false
}
func validateDebianInspectionNode(plan *Plan, build BuildPlan, node Node, artifacts map[string]Artifact) error {
	tool := plan.Definition.SBOMTool
	scanner, ok := plan.CatalogSnapshot.Tools[tool]
	if tool == "" || !ok {
		return e.New(e.CodeProvenanceMissing, "Debian inspection has no selected catalog scanner")
	}
	profile, err := debianInspectionProfile(build, plan.Environment, scanner)
	if err != nil {
		return err
	}
	if node.Container != profile.Image || node.Platform != build.Platform || node.Network != ir.NetworkNone || !containsString(node.Inputs, "tool/"+tool) {
		return e.New(e.CodeEngineCapability, "Debian SBOM changes its offline native inspection profile")
	}
	payloads, results := 0, 0
	for _, id := range node.Inputs {
		artifact := artifacts[id]
		if artifact.BuildTarget != build.Name {
			continue
		}
		switch artifact.Kind {
		case definition.OutputDebianPackage:
			payloads++
		case "producer-result":
			results++
		}
	}
	if payloads != 1 || results != 1 || containsString(node.Inputs, "observation/"+build.Name) {
		return e.New(e.CodeProvenanceMissing, "Debian inspection needs exact original archive/result slots before terminal observation")
	}
	nodes, _ := planIndex(plan)
	terminal, ok := nodes[build.Name+"/terminal-observe"]
	if !ok || terminal.Kind != NodeVerify || !containsString(terminal.Inputs, "sbom/"+build.Name) {
		return e.New(e.CodeProvenanceMissing, "Debian terminal observation requires its original inspected SBOM")
	}
	return nil
}
