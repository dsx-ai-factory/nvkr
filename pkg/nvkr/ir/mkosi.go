// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

// MkosiSourceIdentity binds the source fetch's package acquisition closure. It
// uses the existing vendor-spec family and excludes transport locations. The
// complete graph independently requires identical fetch/build descriptions.
func MkosiSourceIdentity(spec *mkosi.Spec) (string, error) {
	if spec == nil || len(spec.Roots) == 0 {
		return "", e.New(e.CodeInvalidCatalog, "mkosi source requires its recipe")
	}
	bindings := make([]string, 0, len(spec.Files))
	for _, file := range spec.Files {
		bindings = append(bindings, file.Material)
	}
	slices.Sort(bindings)
	if err := spec.Validate("linux/"+spec.Roots[0].Inputs.Architecture, bindings); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindVendorSpec, struct {
		Mkosi *mkosi.LogicalSpec `json:"mkosi"`
	}{spec.Logical()})
}

func validateMkosiNode(node Node) error {
	if err := rejectUnexpectedNodeFields(node, allowFields("requirements", "executionBinding", "component", "builderType", "platform", "network", "workdir",
		"sourceDest", "sourceCommit", "sourceTreeHash", "vendorSpecDigest", "sourceReadOnly", "mkosi", "materials", "outputType", "artifactPath", "version")); err != nil {
		return err
	}
	if !canonicalNodeID.MatchString(node.Component) || node.ID != "build-"+node.Component || len(node.Needs) == 0 ||
		node.OutputType != "oci-image" || node.ArtifactPath != "nvkr-output/runtime" || node.Workdir != "/src" || !node.SourceReadOnly || node.Network != NetworkNone {
		return e.New(e.CodeInvalidCatalog, "mkosi build requires a source dependency, offline recipe and fixed OCI output")
	}
	if !safeRelativePath(node.SourceDest) || !exactGitSHA.MatchString(node.SourceCommit) || !exactGitSHA.MatchString(node.SourceTreeHash) || !safeVersion(node.Version) {
		return e.New(e.CodeUnpinnedSource, "mkosi build requires exact source pins and version")
	}
	if node.ExecutionBinding != nil {
		if err := node.ExecutionBinding.ValidateFor(node.Requirements); err != nil {
			return err
		}
	}
	if err := validateMaterialBindings(node.ID, node.Materials); err != nil {
		return err
	}
	bindings := make([]string, 0, len(node.Materials))
	for _, binding := range node.Materials {
		bindings = append(bindings, binding.Binding)
	}
	if err := node.Mkosi.Validate(node.Platform, bindings); err != nil {
		return err
	}
	want, err := MkosiSourceIdentity(node.Mkosi)
	if err != nil {
		return err
	}
	if node.VendorSpecDigest != want {
		return e.New(e.CodeUnpinnedSource, "mkosi build changes its package acquisition identity")
	}
	return nil
}
