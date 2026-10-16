// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CaptureRetainedDiskPayload checks the retained producer binding, then copies
// the exact observed bytes into a private disk-backed snapshot. The returned
// transport owner survives replacement of the source path and rechecks bytes
// on each stream. The observation still has no successful execution Outcome;
// neither result grants provenance, signing or qualification authority.
// The caller owns Close and must independently bind the observation to its Run.
func CaptureRetainedDiskPayload(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, payloadPath, resultPath string) (artifact *delivery.DiskArtifact, observation DiskPayloadObservation, err error) {
	return captureRetainedDiskPayload(ctx, plan, expected, payloadPath, resultPath, nil)
}

// CaptureRetainedDiskSubject captures transport bytes against a descriptive Run
// subject. It never restores an execution receipt from that saved description.
func CaptureRetainedDiskSubject(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, payloadPath string, subject DiskPayloadObservation) (*delivery.DiskArtifact, DiskPayloadObservation, error) {
	return captureRetainedDiskPayload(ctx, plan, expected, payloadPath, "", &subject)
}

func captureRetainedDiskPayload(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, payloadPath, resultPath string, subject *DiskPayloadObservation) (artifact *delivery.DiskArtifact, observation DiskPayloadObservation, err error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, DiskPayloadObservation{}, e.New(e.CodeEngineCapability, "retained disk capture requires Unix descriptor ownership")
	}
	observation, err = observeRetainedDiskPayload(ctx, plan, expected, payloadPath, resultPath, subject)
	if err != nil {
		return nil, DiskPayloadObservation{}, err
	}
	platform := ""
	for _, build := range plan.Builds {
		if build.Name == expected.Name {
			platform = build.Platform
		}
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return nil, DiskPayloadObservation{}, e.New(e.CodeEngineCapability, "retained disk capture requires a native Linux platform")
	}
	stage, err := os.MkdirTemp("", "nvkr-retained-disk-*")
	if err != nil {
		return nil, DiskPayloadObservation{}, e.Wrap(e.CodeImmutableViolation, err, "creating retained disk snapshot")
	}
	defer func() {
		if cleanupErr := os.RemoveAll(stage); cleanupErr != nil {
			err = errors.Join(err, e.Wrap(e.CodeImmutableViolation, cleanupErr, "removing retained disk snapshot paths"))
		}
		if err == nil && ctx.Err() != nil {
			err = e.Wrap(e.CodeTimeout, ctx.Err(), "retained disk capture canceled")
		}
		if err != nil {
			if artifact != nil {
				err = errors.Join(err, artifact.Close())
			}
			artifact, observation = nil, DiskPayloadObservation{}
		}
	}()
	directory, err := os.Open(stage)
	if err != nil {
		return nil, DiskPayloadObservation{}, e.Wrap(e.CodeImmutableViolation, err, "opening retained disk scratch")
	}
	available, capacityErr := securepath.AvailableDiskBackedBytes(ctx, directory)
	if err := errors.Join(capacityErr, directory.Close()); err != nil {
		return nil, DiskPayloadObservation{}, e.Wrap(e.CodeEngineCapability, err, "retained disk capture requires disk-backed writable TMPDIR")
	}
	// Include bounded OCI metadata and filesystem overhead. This is a headroom
	// observation, not a reservation; later write failures still fail capture.
	if available < uint64(observation.Size)+(8<<20) {
		return nil, DiskPayloadObservation{}, e.New(e.CodeEngineCapability, "insufficient disk-backed scratch for retained disk capture")
	}
	layout := filepath.Join(stage, "layout")
	_, err = delivery.BuildDiskArtifact(ctx, layout, delivery.DiskArtifactRequest{
		Path: payloadPath, Digest: observation.Digest, Size: observation.Size, Platform: platform,
		Format: observation.Format, Encryption: observation.Encryption, ArtifactType: delivery.MediaDiskImage,
	})
	if err != nil {
		return nil, DiskPayloadObservation{}, err
	}
	artifact, err = delivery.OpenSingleDiskArtifact(ctx, layout, platform, observation.Size)
	if err != nil {
		return nil, DiskPayloadObservation{}, err
	}
	if err = artifact.RequireStandardProfile(ctx); err != nil {
		return artifact, observation, err
	}
	return artifact, observation, nil
}
