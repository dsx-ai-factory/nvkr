// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"regexp"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

const imageIndexBuildSchema = "nvkr.dev/image-index-build/v1"

var imageIndexComponent = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

// ImageIndexBuildChild describes one concrete-platform logical build. Manifest
// observations, export archives, output labels and transport cannot enter this DTO.
// A digest description is not proof that the child build executed.
type ImageIndexBuildChild struct {
	Platform    string `json:"platform"`
	BuildDigest string `json:"buildDigest"`
	Component   string `json:"component"`
}

type imageIndexBuild struct {
	SchemaVersion     string                 `json:"schemaVersion"`
	RequiredPlatforms []string               `json:"requiredPlatforms"`
	Children          []ImageIndexBuildChild `json:"children"`
}

// ImageIndexBuildDigest derives a logical aggregate identity from the exact
// required native-build vector. It rejects noncanonical or incomplete membership;
// callers must independently authenticate/recompute each child's Build Digest.
func ImageIndexBuildDigest(requiredPlatforms []string, children []ImageIndexBuildChild) (string, error) {
	if err := target.ValidatePlatforms(requiredPlatforms); err != nil {
		return "", err
	}
	if len(children) != len(requiredPlatforms) {
		return "", e.New(e.CodeInvalidCatalog, "image index build children differ from its required platform set")
	}
	component := ""
	for i, child := range children {
		if child.Platform != requiredPlatforms[i] || !canonical.IsDigest(child.BuildDigest) || !imageIndexComponent.MatchString(child.Component) {
			return "", e.New(e.CodeInvalidCatalog, "image index build child %q has invalid platform, component or Build Digest", child.Platform)
		}
		if component != "" && child.Component != component {
			return "", e.New(e.CodeInvalidCatalog, "image index build children must select the same component")
		}
		component = child.Component
	}
	return canonical.Digest(canonical.KindBuild, imageIndexBuild{
		SchemaVersion: imageIndexBuildSchema, RequiredPlatforms: requiredPlatforms, Children: children,
	})
}
