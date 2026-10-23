// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// localQualificationDisks owns only disk transport snapshots. Ordinary output
// checks still reference their original workspace; this is not a complete
// publication, authenticated source or qualification receipt.
type localQualificationDisks struct {
	members map[string]*delivery.DiskArtifact
}

func (disks *localQualificationDisks) Close() error {
	if disks == nil {
		return nil
	}
	names := make([]string, 0, len(disks.members))
	for name := range disks.members {
		names = append(names, name)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		errs = append(errs, disks.members[name].Close())
	}
	return errors.Join(errs...)
}

type localDiskCapture func(context.Context, *factory.Plan, factory.LifecycleOutputPlan, string, factory.DiskPayloadObservation) (*delivery.DiskArtifact, factory.DiskPayloadObservation, error)

func captureLocalQualificationDisks(ctx context.Context, workspace string, inputs *localQualificationInputs) (*localQualificationDisks, error) {
	return captureLocalQualificationDisksWith(ctx, workspace, inputs, factory.CaptureRetainedDiskSubject)
}

// Capture matches every disk snapshot to its original Run slot and retains
// lifecycle's ordinary slot and outer-publication checks. The complete capture's
// factory publication owner verifies ordinary producer and package bytes once.
// Any refusal closes all previously captured disks.
func captureLocalQualificationDisksWith(ctx context.Context, workspace string, inputs *localQualificationInputs, capture localDiskCapture) (*localQualificationDisks, error) {
	disks := &localQualificationDisks{members: map[string]*delivery.DiskArtifact{}}
	observeDisk := func(ctx context.Context, plan *factory.Plan, slot factory.LifecycleOutputPlan, payload string, subject factory.DiskPayloadObservation) (factory.DiskPayloadObservation, error) {
		owner, observed, err := capture(ctx, plan, slot, payload, subject)
		if err != nil {
			if owner != nil {
				err = errors.Join(err, owner.Close())
			}
			return factory.DiskPayloadObservation{}, err
		}
		if owner == nil {
			return factory.DiskPayloadObservation{}, e.New(e.CodeGateUnmet, "local disk capture returned no transport owner")
		}
		if _, exists := disks.members[slot.Name]; exists {
			return factory.DiskPayloadObservation{}, errors.Join(e.New(e.CodeReleaseIncomplete, "local capture repeated a named disk"), owner.Close())
		}
		disks.members[slot.Name] = owner
		inspection := owner.Inspection()
		if inspection.PayloadDigest != observed.Digest || inspection.Size != observed.Size || inspection.Encryption != observed.Encryption || observed.Outcome != "" {
			return factory.DiskPayloadObservation{}, e.New(e.CodeVerifyFailed, "local disk snapshot differs from retained observation")
		}
		return observed, nil
	}
	err := checkLocalQualificationPayloadsWith(ctx, workspace, inputs, observeDisk,
		func(ctx context.Context, slot requiredOutput, want Artifact, artifacts map[string]factory.Artifact) error {
			member, err := ordinaryOutputMember(slot, artifacts)
			if err != nil {
				return err
			}
			// Factory's subsequent held capture must match this recorded subject.
			// APT observations use the repository semantic digest in both fields.
			return checkOutputPublicationMember(ctx, slot, member, artifacts, want.Digest, want.Digest)
		})
	if err != nil {
		return nil, errors.Join(err, disks.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(e.Wrap(e.CodeTimeout, err, "local disk set capture canceled"), disks.Close())
	}
	return disks, nil
}
