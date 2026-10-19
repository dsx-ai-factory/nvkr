// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// openLocalLayoutConsumer needs only transported evidence and OCI layouts. It
// never opens a producer workspace or accepts producer owners. Locations supply
// no trust; the independently selected policy authenticates the exact closure.
func openLocalLayoutConsumer(ctx context.Context, ordinary string, disks map[string]string, policy LocalQualificationPolicy, binary string, files map[string][]byte) (*localQualificationConsumer, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "local layout verification requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local layout verification canceled")
	}
	frozen, err := freezeLocalEvidenceFiles(files)
	if err != nil {
		return nil, err
	}
	trusted, err := policy.freeze()
	if err != nil {
		return nil, err
	}
	document, signature := frozen["build-evidence.json"], frozen["build-evidence.sig"]
	if err := validateLocalBuildDocument(document, trusted); err != nil {
		return nil, err
	}
	verifier, err := sign.NewLocalCosign(binary, trusted.CosignDigest)
	if err != nil {
		return nil, err
	}
	if err := verifier.VerifyDocument(ctx, document, signature, []byte(trusted.BuilderKey)); err != nil {
		return nil, err
	}
	inputs, err := replayLocalQualificationInputs(ctx, frozen["plan.json"], frozen["lifecycle.json"], frozen["compile-inputs.json"])
	if err != nil {
		return nil, err
	}
	if err := trusted.RequirePlan(inputs.plan); err != nil {
		return nil, err
	}
	var description localBuildEvidence
	if err := canonical.UnmarshalExact(document, &description); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding authenticated local build evidence")
	}
	capture, err := captureLocalTransportLayouts(ctx, inputs, description, ordinary, disks, frozen["disk-materials.json"])
	if err != nil {
		return nil, err
	}
	expected, err := prepareLocalBuildEvidence(ctx, inputs, capture, trusted)
	if err != nil {
		return nil, errors.Join(err, capture.Close())
	}
	if !bytes.Equal(expected, document) {
		return nil, errors.Join(e.New(e.CodeVerifyFailed, "transported local payloads or inputs differ from signed build evidence"), capture.Close())
	}
	build := newLocalVerifiedBuild(ctx, inputs, capture, string(document), trusted)
	build.signature = string(signature)
	return finishLocalQualificationConsumer(ctx, build, binary, frozen)
}

func captureLocalTransportLayouts(ctx context.Context, inputs *localQualificationInputs, description localBuildEvidence, ordinary string, disks map[string]string, materials []byte) (capture *localQualificationCapture, err error) {
	// Admit complete locator membership before any layout I/O.
	wanted := map[string]localBuildEvidenceOutput{}
	ordinaryCount := 0
	for _, output := range description.Outputs {
		if output.Artifact.Kind != OutputDiskImage {
			ordinaryCount++
			continue
		}
		if _, exists := wanted[output.Artifact.Name]; exists {
			return nil, e.New(e.CodeReleaseIncomplete, "duplicate disk transport member")
		}
		if output.Artifact.Disk == nil || disks[output.Artifact.Name] == "" {
			return nil, e.New(e.CodeReleaseIncomplete, "missing disk transport member")
		}
		wanted[output.Artifact.Name] = output
	}
	if len(wanted) != len(disks) || (ordinaryCount > 0) != (ordinary != "") || (ordinaryCount > 0) != (description.OrdinaryRoot != "") {
		return nil, e.New(e.CodeReleaseIncomplete, "local transport locations differ from complete signed membership")
	}
	capture = &localQualificationCapture{ordinaryRoot: description.OrdinaryRoot, materialBytes: string(materials), disks: &localQualificationDisks{members: map[string]*delivery.DiskArtifact{}}}
	defer func() {
		if err != nil {
			err = errors.Join(err, capture.Close())
			capture = nil
		}
	}()
	if ordinaryCount > 0 {
		capture.ordinary, err = delivery.OpenPublication(ctx, ordinary, description.OrdinaryRoot)
		if err != nil {
			return capture, err
		}
	}
	// Signed output order is canonical and is rechecked against the replayed Run.
	for _, output := range description.Outputs {
		if output.Artifact.Kind != OutputDiskImage {
			continue
		}
		platform := ""
		for _, build := range inputs.plan.Builds {
			if build.Name == output.Artifact.Name {
				platform = build.Platform
			}
		}
		owner, openErr := delivery.OpenDiskArtifact(ctx, disks[output.Artifact.Name], output.TransportDigest, platform, output.Artifact.Disk.Size)
		if openErr != nil {
			return capture, openErr
		}
		capture.disks.members[output.Artifact.Name] = owner
	}
	return capture, nil
}
