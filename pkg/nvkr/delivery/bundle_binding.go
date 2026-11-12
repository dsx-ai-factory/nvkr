// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"encoding/json"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BundleBinding retains exact producer index bytes so consumers can recompute
// the relation between the layout build subject and the published manifest.
// It is descriptive until its containing Release has been authenticated.
type BundleBinding struct {
	Component string `json:"component"`
	Index     []byte `json:"index"`
}

func validateBundleBinding(object PublishedObject) error {
	output := object.Output
	if output == nil || output.Bundle == nil {
		return e.New(e.CodeVerifyFailed, "imported bundle omits its producer index binding")
	}
	binding := output.Bundle
	if output.Format != outputFormatBundle || object.ArtifactType != MediaAICRArtifact ||
		!slices.Contains(output.Components, binding.Component) || len(binding.Index) == 0 || len(binding.Index) > 1<<20 {
		return e.New(e.CodeVerifyFailed, "bundle producer binding has an invalid shape")
	}
	if err := canonical.ValidateJSONDocument(binding.Index); err != nil {
		return err
	}
	var index ociIndex
	if err := json.Unmarshal(binding.Index, &index); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading bundle producer index")
	}
	if index.SchemaVersion != 2 || len(index.Manifests) != 1 || index.Manifests[0].Digest != object.DescriptorDigest ||
		index.Manifests[0].MediaType != MediaOCIManifest || index.Manifests[0].Size != object.Size {
		return e.New(e.CodeVerifyFailed, "bundle producer index does not name the published manifest")
	}
	for _, subject := range output.BuildSubjects {
		if subject.Component == binding.Component && subject.Digest == digestBytes(binding.Index) {
			return nil
		}
	}
	return e.New(e.CodeVerifyFailed, "bundle producer index differs from its build subject")
}
