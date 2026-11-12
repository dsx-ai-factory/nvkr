// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociformat"
)

type OCIRepresentation = ociformat.Representation

const (
	OCIArtifactV1               = ociformat.ArtifactV1
	OCIHelmStandardV1           = ociformat.HelmStandardV1
	OCINVKRCompatibleV1         = ociformat.NVKRCompatibleV1
	ociRepresentationAnnotation = "dev.nvkr.oci.representation"
)

func admitOCIRepresentation(value OCIRepresentation) error {
	if !ociformat.Valid(value) {
		return e.New(e.CodeEngineCapability, "OCI publication representation %q is unsupported", value)
	}
	return nil
}

// OCIRepresentation describes the format selected for newly constructed NVKR
// wrappers. It does not describe or authorize conversion of every member.
func (root PublicationRoot) OCIRepresentation() OCIRepresentation { return root.ociRepresentation }

func inspectOCIRepresentation(index ociIndex) (OCIRepresentation, error) {
	value := OCIRepresentation(index.Annotations[ociRepresentationAnnotation])
	if !ociformat.Valid(value) {
		return "", e.New(e.CodeVerifyFailed, "publication has an unsupported OCI representation")
	}
	compatible := value == OCINVKRCompatibleV1 || value == OCIHelmStandardV1
	if compatible != (index.ArtifactType == "") {
		return "", e.New(e.CodeVerifyFailed, "publication wrapper differs from its explicit OCI representation")
	}
	return value, nil
}

func validateRootRepresentation(value OCIRepresentation, manifest ociManifest) error {
	configType := MediaPublicationRootConfig
	if value == OCIHelmStandardV1 {
		configType = MediaOctetStream
	}
	if manifest.Config.MediaType != configType {
		return e.New(e.CodeVerifyFailed, "root config differs from its selected OCI representation")
	}
	if (value == OCINVKRCompatibleV1 || value == OCIHelmStandardV1) != (manifest.ArtifactType == "") {
		return e.New(e.CodeVerifyFailed, "root manifest differs from its selected OCI representation")
	}
	return nil
}
