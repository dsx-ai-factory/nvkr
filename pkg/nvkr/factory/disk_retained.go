// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ObserveRetainedDiskPayload checks a retained raw QCOW2 against its exact
// compiled root producer and saved result. It returns current-byte descriptions
// only: Outcome is deliberately empty. It cannot restore execution, material
// provenance, signing, publication or qualification authority from paths.
// The local lifecycle owner supplies its deterministic retained payload path;
// paths embedded in the result never select bytes.
func ObserveRetainedDiskPayload(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, payloadPath, resultPath string) (DiskPayloadObservation, error) {
	return observeRetainedDiskPayload(ctx, plan, expected, payloadPath, resultPath, nil)
}

// ObserveRetainedDiskSubject measures the saved delivery bytes against a Run
// description and the compiled producer. Local disk execution retains that
// description, not a producer-result file. Neither the supplied description nor
// this check authenticates execution; the returned Outcome is always empty.
func ObserveRetainedDiskSubject(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, payloadPath string, subject DiskPayloadObservation) (DiskPayloadObservation, error) {
	return observeRetainedDiskPayload(ctx, plan, expected, payloadPath, "", &subject)
}

func observeRetainedDiskPayload(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, payloadPath, resultPath string, subject *DiskPayloadObservation) (DiskPayloadObservation, error) {
	if ctx == nil || plan == nil || payloadPath == "" || (resultPath == "" && subject == nil) {
		return DiskPayloadObservation{}, e.New(e.CodeUsage, "retained disk observation requires context, Plan and explicit payload/result paths")
	}
	if err := ctx.Err(); err != nil {
		return DiskPayloadObservation{}, e.Wrap(e.CodeTimeout, err, "retained disk observation canceled")
	}
	if err := plan.Validate(); err != nil {
		return DiskPayloadObservation{}, err
	}
	matched := false
	for _, binding := range plan.LifecycleOutputs {
		if binding.Name == expected.Name {
			matched = binding.Kind == PackageDiskImage && binding.Kind == expected.Kind && binding.ArtifactID == expected.ArtifactID &&
				binding.BuildDigest == expected.BuildDigest && slices.Equal(binding.Components, expected.Components) && expected.ImageIndex == nil
		}
	}
	if !matched {
		return DiskPayloadObservation{}, e.New(e.CodePipelineDrift, "retained disk changes its exact compiled output binding")
	}
	_, artifacts := planIndex(plan)
	raw, ok := artifacts[expected.ArtifactID]
	if !ok || raw.Kind != PackageDiskImage || raw.BuildTarget != expected.Name || raw.Component == "" {
		return DiskPayloadObservation{}, e.New(e.CodePipelineDrift, "retained disk has no exact compiled raw producer")
	}
	build, producer, err := plan.producerInstruction(planNode(plan, raw.Producer))
	if err != nil {
		return DiskPayloadObservation{}, err
	}
	if build.Name != expected.Name || build.BuildDigest != expected.BuildDigest || producer.Component != raw.Component ||
		producer.Disk == nil || producer.Disk.Format != "qcow2" ||
		(producer.Disk.Encryption != "none" && producer.Disk.Encryption != "luks2") {
		return DiskPayloadObservation{}, e.New(e.CodePipelineDrift, "retained disk differs from its named build or QCOW2 producer")
	}
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		return DiskPayloadObservation{}, err
	}
	var digest string
	var size int64
	if subject != nil {
		if subject.PlanDigest != planDigest || subject.BuildDigest != build.BuildDigest ||
			subject.Producer != producer.ID || subject.Component != raw.Component ||
			subject.Format != producer.Disk.Format || subject.Encryption != producer.Disk.Encryption ||
			!slices.Equal(subject.Components, expected.Components) || !canonical.IsDigest(subject.Digest) || subject.Size <= 0 || subject.Outcome != "" {
			return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed, "retained disk subject differs from its exact compiled producer")
		}
		digest, size = subject.Digest, subject.Size
	} else {
		result, err := readProducerResult(ctx, resultPath)
		if err != nil {
			return DiskPayloadObservation{}, err
		}
		if err := validateDiskProducerDescription(producer, result); err != nil {
			return DiskPayloadObservation{}, err
		}
		digest, size = result.SHA256, result.Size
	}
	if size > 64<<30 {
		return DiskPayloadObservation{}, e.New(e.CodeEngineCapability, "retained disk exceeds the supported observation bound")
	}
	source, info, err := securepath.OpenRegular(payloadPath)
	if err != nil {
		return DiskPayloadObservation{}, e.Wrap(e.CodeVerifyFailed, err, "opening retained disk payload")
	}
	defer source.Close()
	if info.Size() != size {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed, "retained disk size differs from its producer record")
	}
	stream := &diskObservationWriter{ctx: ctx, digest: sha256.New()}
	_, err = io.Copy(stream, io.NewSectionReader(source, 0, info.Size()))
	if err != nil {
		if ctx.Err() != nil {
			return DiskPayloadObservation{}, e.Wrap(e.CodeTimeout, ctx.Err(), "retained disk observation canceled")
		}
		return DiskPayloadObservation{}, e.Wrap(e.CodeVerifyFailed, err, "reading retained disk payload")
	}
	after, err := source.Stat()
	if err != nil || !securepath.SameMetadata(info, after) || stream.size != size ||
		"sha256:"+hex.EncodeToString(stream.digest.Sum(nil)) != digest {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed, "retained disk bytes differ from the recorded producer subject")
	}
	if err := validateQCOW2Header(stream.header, stream.size); err != nil {
		return DiskPayloadObservation{}, e.Wrap(e.CodeVerifyFailed, err, "retained disk has unsupported QCOW2 framing")
	}
	return DiskPayloadObservation{Digest: digest, Size: size, BuildDigest: build.BuildDigest,
		Components: append([]string(nil), expected.Components...), Producer: producer.ID, Component: raw.Component,
		Format: producer.Disk.Format, Encryption: producer.Disk.Encryption, PlanDigest: planDigest}, nil
}
