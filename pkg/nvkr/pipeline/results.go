// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const maxResultBytes = 1 << 20

// ObserveResult reopens one producer's recorded result and its expected output.
// The caller supplies the paths from a validated execution plan; paths recorded by
// the producer never select bytes. This checks descriptive metadata and current
// bytes only, and issues no execution qualification or terminal receipt.
func ObserveResult(ctx context.Context, node ir.Node, payloadPath, resultPath string) (ArtifactResult, error) {
	if err := requireRunContext(ctx, "producer result observation"); err != nil {
		return ArtifactResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ArtifactResult{}, e.Wrap(e.CodeTimeout, err, "producer result observation canceled")
	}
	if node.Kind != ir.KindBuild && node.Kind != ir.KindPull {
		return ArtifactResult{}, e.New(e.CodeVerifyFailed, "node %q is not an artifact producer", node.ID)
	}
	result, err := readArtifactResultContext(ctx, resultPath)
	if err != nil {
		return ArtifactResult{}, err
	}
	if result.Component != node.Component || result.OutputType != node.OutputType ||
		!canonical.IsDigest(result.SHA256) || result.Size < 0 {
		return ArtifactResult{}, e.New(e.CodeVerifyFailed, "producer result %q has no exact component, output type or byte identity", node.ID)
	}
	if node.Kind == ir.KindBuild {
		if result.Toolchain != node.Toolchain || result.BuilderType != node.BuilderType || result.Path != node.ArtifactPath ||
			result.Ref != "" || result.Packaged {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed, "build result %q differs from its declared producer", node.ID)
		}
	} else {
		if result.SHA256 != node.Digest || result.EngineClass != ir.EnginePrebuiltAcquire {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed, "pull result %q differs from its pinned acquisition", node.ID)
		}
		if node.OutputType == "oci-image" {
			wantRef, err := ir.NativeImageReference(node)
			if err != nil {
				return ArtifactResult{}, err
			}
			if result.Ref != wantRef {
				return ArtifactResult{}, e.New(e.CodeVerifyFailed, "pull result %q differs from its pinned image reference", node.ID)
			}
			size, err := verifyOCILayoutAndSize(ctx, payloadPath, node.Digest)
			if err != nil {
				return ArtifactResult{}, err
			}
			if result.Size != size {
				return ArtifactResult{}, e.New(e.CodeVerifyFailed, "pull result %q differs from its observed OCI closure size", node.ID)
			}
			result.TransportPath = payloadPath
			result.Path = result.Ref
			return result, nil
		}
		if result.Ref != "" || result.Packaged {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed, "pull result %q does not describe a raw acquired file", node.ID)
		}
	}
	if node.BuilderType == ir.BuilderPinnedTool && node.OutputType == ir.OutputOCILayout {
		result.TransportPath = payloadPath
		payloadPath = filepath.Join(payloadPath, "index.json")
	}

	digest, size, _, err := hashRegularBeneathContext(ctx, filepath.Dir(payloadPath), filepath.Base(payloadPath))
	if err != nil {
		return ArtifactResult{}, mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "observing producer %q bytes", node.ID), "producer result observation")
	}
	if result.SHA256 != digest || result.Size != size {
		return ArtifactResult{}, e.New(e.CodeVerifyFailed, "producer %q bytes differ from its recorded digest or size", node.ID)
	}
	result.AbsPath = payloadPath
	if node.OutputType != ir.OutputOCILayout {
		result.TransportPath = ""
	}
	if node.Kind == ir.KindPull {
		result.Path = payloadPath
	}
	return result, nil
}

// AuthenticateResultForConsumption admits one exact producer payload/result pair
// for use by a downstream producer. It strengthens ObserveResult with the
// producer-kind metadata invariants available without a terminal execution
// receipt. Qualified-execution claims are rejected here because this boundary
// does not own the receipt needed to authenticate them.
func AuthenticateResultForConsumption(ctx context.Context, node ir.Node, payloadPath, resultPath string) (ArtifactResult, error) {
	result, err := ObserveResult(ctx, node, payloadPath, resultPath)
	if err != nil {
		return ArtifactResult{}, err
	}
	switch node.Kind {
	case ir.KindBuild:
		if !canonical.IsDigest(result.InputDigest) {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed,
				"build result %q has no exact prepared-source identity", node.ID)
		}
		// A pinned-tool producer's input is its executable (ADR-27 §2).
		if node.BuilderType == ir.BuilderPinnedTool && result.InputDigest != node.Digest {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed,
				"pinned-tool result %q does not record its pinned executable %s", node.ID, node.Digest)
		}
		if len(node.Materials) != 0 && !canonical.IsDigest(result.MaterialManifestDigest) {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed,
				"build result %q has no exact material-manifest identity", node.ID)
		}
		if len(node.Materials) == 0 && result.MaterialManifestDigest != "" {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed,
				"build result %q records undeclared material inputs", node.ID)
		}
		if result.Qualification != ArtifactUnqualified {
			return ArtifactResult{}, e.New(e.CodePreflightFailed,
				"build result %q requires its execution receipt before downstream consumption", node.ID)
		}
		if result.ProfileDigest != "" || result.ExecutionBinding != "" ||
			(result.EngineClass != "" && result.EngineClass != node.BuilderType) {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed,
				"unqualified build result %q carries unauthenticated execution metadata", node.ID)
		}
	case ir.KindPull:
		if result.Qualification != ArtifactProfileFreeAcquisition || result.BuilderType != "" ||
			result.ProfileDigest != "" || result.ExecutionBinding != "" ||
			result.EngineClass != ir.EnginePrebuiltAcquire {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed,
				"pull result %q is not an exact profile-free acquisition", node.ID)
		}
	default:
		return ArtifactResult{}, e.New(e.CodeInternal,
			"producer result %q has unsupported kind %q", node.ID, node.Kind)
	}
	return result, nil
}

func readArtifactResultContext(ctx context.Context, path string) (ArtifactResult, error) {
	var result ArtifactResult
	raw, err := readRegularLimitedContext(ctx, path, maxResultBytes)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return result, e.Wrap(e.CodeTimeout, ctx.Err(), "reading producer result %s", path)
		}
		return result, e.Wrap(e.CodeVerifyFailed, err, "reading producer result %s", path)
	}
	if len(raw) > maxResultBytes {
		return result, e.New(e.CodeVerifyFailed, "producer result %s exceeds %d bytes", path, maxResultBytes)
	}
	if err := canonical.UnmarshalExact(raw, &result); err != nil {
		return result, e.Wrap(e.CodeVerifyFailed, err, "decoding producer result %s", path)
	}
	return result, nil
}
