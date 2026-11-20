// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func (owner *LocalRunResult) scanDiskPackages(ctx context.Context, _ *Plan, build BuildPlan, trace *diskMaterialTrace, payload *diskPayload,
	inspection *sbom.DiskInspectionRuntime, profile definition.DiskInspectionProfile,
) (diskEvidenceScan, error) {
	artifact, ok := payload.content.(*pipeline.PackerArtifact)
	if !ok || artifact == nil {
		return diskEvidenceScan{}, e.New(e.CodeVerifyFailed, "package inventory requires the original held Packer artifact")
	}
	subject := trace.description.Subject
	if artifact.Size() != subject.Size {
		return diskEvidenceScan{}, e.New(e.CodeVerifyFailed, "package inventory subject size changed")
	}
	inventory, err := inspection.InspectPackerInstalledPackages(ctx, artifact, subject.Format, subject.Encryption)
	if err != nil {
		return diskEvidenceScan{}, err
	}
	digest, size, inspector, codec := inventory.Binding()
	image, actualInspector, actualCodec, err := inspection.BindingContext(ctx)
	if err != nil {
		return diskEvidenceScan{}, err
	}
	if digest != subject.Digest || size != subject.Size || inventory.RuntimeBinding() != image ||
		inspector != actualInspector || codec != actualCodec || inspector != profile.InspectorDigest || codec != profile.CodecDigest {
		return diskEvidenceScan{}, e.New(e.CodeVerifyFailed, "package inventory changed its exact subject or inspector")
	}
	hashes := make(map[string]string, len(trace.description.Members))
	for _, member := range trace.description.Members {
		hashes[member.Component] = member.Digest
	}
	documents, err := diskBuildDocuments(build, hashes)
	if err != nil {
		return diskEvidenceScan{}, err
	}
	bom, err := sbom.DiskPackageDocument(build.BuildDigest, subject.Component, inventory)
	if err != nil {
		return diskEvidenceScan{}, err
	}
	bom, err = sbom.WithSourceSubject(bom, documents[subject.Component])
	if err != nil {
		return diskEvidenceScan{}, err
	}
	documents[subject.Component] = bom
	raw := make(map[string][]byte, len(documents))
	for _, name := range sortedKeys(documents) {
		raw[name], err = documents[name].Marshal()
		if err != nil {
			return diskEvidenceScan{}, err
		}
	}
	return diskEvidenceScan{subject: digest, size: size, profile: profile, bom: bom, documents: raw}, nil
}
