// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"slices"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// GitHubDiskBuildOnly identifies the explicitly unqualified native ARC route.
// Its Actions artifacts and Run are byte observations, never release authority.
// Complete Plan and native execution admission remain required before work.
func GitHubDiskBuildOnly(plan *Plan) bool {
	return plan != nil && plan.Environment.GitHubARC == "native-kvm-v1" &&
		plan.Qualification == TierStaticUnqualified && plan.Environment.Publication == nil &&
		plan.Environment.Signing == nil && plan.Environment.DiskEvidence == "" && len(plan.Routes) == 0
}

func githubDiskInspectionRequired(plan *Plan) bool {
	return !GitHubDiskBuildOnly(plan) && !diskBuildProvenance(plan)
}

// Keep the original live owner through exact-byte export. The graph's ordinary
// carriers transport the raw QCOW2, result and terminal observation afterwards.
func completeGitHubDiskBuild(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, owner *LocalRunResult) error {
	if ctx == nil || owner == nil {
		return e.New(e.CodeUsage, "hosted disk build completion requires context and live owner")
	}
	if !GitHubDiskBuildOnly(plan) {
		return e.New(e.CodeEngineCapability, "hosted raw disk completion requires the build-only ARC route")
	}
	observation, result, err := owner.inspectDiskLifecycleMetadata(ctx, plan, expected)
	if err != nil {
		return err
	}
	_, artifacts := planIndex(plan)
	payload := artifacts[expected.ArtifactID]
	record := artifacts[diskPublicationProducerResultID(expected, payload)]
	if payload.Kind != PackageDiskImage || record.Kind != "producer-result" || record.Component != observation.Component || record.Producer != payload.Producer {
		return e.New(e.CodePipelineDrift, "hosted raw disk lacks its exact payload and producer result")
	}
	if err := commitGitHubDiskFile(ctx, payload.Path, func(file *os.File) error {
		_, err := owner.ExportDiskLifecyclePayload(ctx, plan, expected, file)
		return err
	}); err != nil {
		return err
	}
	raw, err := canonical.Marshal(result)
	if err != nil {
		return err
	}
	return commitGitHubDiskFile(ctx, record.Path, func(file *os.File) error {
		_, err := copyContext(ctx, file, bytes.NewReader(raw))
		return err
	})
}

// Every successful export is no-replace and durable beneath held parents. A
// partial attempt cannot complete: the provider outcome and all exact carriers
// must succeed before the hosted finalizer records a complete Run.
func commitGitHubDiskFile(ctx context.Context, path string, write func(*os.File) error) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "hosted disk export canceled")
	}
	parent, target, err := securepath.CreateParentBeneath(".", path)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "hold hosted disk export parent")
	}
	defer func() { returnErr = joinFactoryCleanupError(returnErr, parent.Close(), "close disk export parent") }()
	var existing unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), target, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return e.New(e.CodeImmutableViolation, "hosted disk export already exists")
	} else if err != unix.ENOENT {
		return e.Wrap(e.CodeImmutableViolation, err, "check hosted disk export destination")
	}
	file, temporary, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "stage hosted disk export")
	}
	defer func() {
		_ = file.Close()
		if temporary != "" {
			returnErr = joinFactoryCleanupError(returnErr, unix.Unlinkat(int(parent.Fd()), temporary, 0), "remove incomplete disk export")
		}
	}()
	if err := write(file); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "export held hosted disk bytes"))
	}
	if err := file.Chmod(0o444); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "protect hosted disk export")
	}
	if err := file.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persist hosted disk export")
	}
	if err := file.Close(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "close hosted disk export")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "hosted disk export canceled before commit")
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), target); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "commit hosted disk export")
	}
	temporary = ""
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persist hosted disk export directory")
	}
	return nil
}

// ObserveHostedDiskBuild checks restored bytes and their exact compiled producer
// description. It issues no live receipt and cannot authorize disk publication.
// The caller must also validate complete hosted restore/terminal outcomes.
func ObserveHostedDiskBuild(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, artifacts map[string]Artifact) (observation DiskPayloadObservation, returnErr error) {
	if ctx == nil || plan == nil {
		return observation, e.New(e.CodeUsage, "hosted disk observation requires context and Plan")
	}
	if err := ctx.Err(); err != nil {
		return observation, e.Wrap(e.CodeTimeout, err, "hosted disk observation canceled")
	}
	if err := plan.Validate(); err != nil {
		return observation, err
	}
	if !GitHubDiskBuildOnly(plan) {
		return observation, e.New(e.CodeEngineCapability, "raw hosted disk observation requires the build-only ARC route")
	}
	if err := admitGitHubRequirements(plan, "hosted disk observation"); err != nil {
		return observation, err
	}
	matched := false
	for _, binding := range plan.LifecycleOutputs {
		if binding.Name == expected.Name && binding.Kind == PackageDiskImage && binding.ArtifactID == expected.ArtifactID &&
			binding.BuildDigest == expected.BuildDigest && slices.Equal(binding.Components, expected.Components) {
			matched = true
		}
	}
	_, planned := planIndex(plan)
	if !matched || !plan.hasCompiledDiskTarget(expected.Name, planned) {
		return observation, e.New(e.CodePipelineDrift, "hosted disk observation changes its exact lifecycle binding")
	}
	payload := artifacts[expected.ArtifactID]
	recordID := diskPublicationProducerResultID(expected, planned[expected.ArtifactID])
	record := artifacts[recordID]
	for _, id := range []string{expected.ArtifactID, recordID} {
		actual := artifacts[id]
		wanted, ok := planned[id]
		wanted.Path = actual.Path // The finalizer explicitly relocates restored artifacts.
		if !ok || wanted != actual || actual.Path == "" {
			return observation, e.New(e.CodePipelineDrift, "hosted disk observation changes its raw payload or producer record")
		}
	}
	_, producer, err := plan.producerInstruction(planNode(plan, payload.Producer))
	if err != nil {
		return observation, err
	}
	result, err := readProducerResult(ctx, record.Path)
	if err != nil {
		return observation, err
	}
	if err := validateDiskProducerDescription(producer, result); err != nil {
		return observation, err
	}
	file, before, err := securepath.OpenRegular(payload.Path)
	if err != nil {
		return observation, e.Wrap(e.CodeVerifyFailed, err, "hold restored hosted disk")
	}
	defer func() { returnErr = joinFactoryCleanupError(returnErr, file.Close(), "close restored hosted disk") }()
	if before.Size() != result.Size {
		return observation, e.New(e.CodeVerifyFailed, "restored hosted disk size differs from its producer result")
	}
	stream := &diskObservationWriter{ctx: ctx, digest: sha256.New()}
	if _, err := copyContext(ctx, stream, io.LimitReader(file, result.Size+1)); err != nil {
		return observation, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "observe restored hosted disk"))
	}
	digest := "sha256:" + hex.EncodeToString(stream.digest.Sum(nil))
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(before, after) || stream.size != result.Size || digest != result.SHA256 {
		return observation, e.New(e.CodeVerifyFailed, "restored hosted disk differs from its exact producer bytes")
	}
	if err := validateQCOW2Header(stream.header, stream.size); err != nil {
		return observation, e.Wrap(e.CodeVerifyFailed, err, "restored hosted disk lacks admitted QCOW2 framing")
	}
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		return observation, err
	}
	return DiskPayloadObservation{Digest: digest, Size: stream.size, BuildDigest: expected.BuildDigest,
		Components: append([]string(nil), expected.Components...), Producer: producer.ID, Component: producer.Component,
		Format: producer.Disk.Format, Encryption: producer.Disk.Encryption, Outcome: "succeeded", PlanDigest: planDigest}, nil
}
