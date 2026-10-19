// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Qualification checks every payload but retains only controls, evidence and
// executables. This private view intentionally cannot export layouts or disks.
// Ordinary VerifiedRelease remains backed by a complete delivery.Publication.
type qualificationPublication interface {
	publicationVerificationView
	Close() error
	Description(string) (delivery.PublicationDescription, error)
	VisitEvidence(context.Context, func(context.Context, delivery.EvidenceBinding, []byte) error) error
	ExecutableBundle(string) (delivery.BinaryBundle, error)
	WriteExecutableMember(context.Context, string, string, io.Writer) (int64, error)
}

type qualificationMaterializer func(context.Context, string) (qualificationPublication, error)

type verifiedQualificationPublication struct {
	mu          sync.RWMutex
	valid       bool
	release     Release
	publication qualificationPublication
}

func (verified *verifiedQualificationPublication) Description() (Release, error) {
	if verified == nil {
		return Release{}, e.New(e.CodeGateUnmet, "qualification publication is not verified")
	}
	verified.mu.RLock()
	defer verified.mu.RUnlock()
	if !verified.valid {
		return Release{}, e.New(e.CodeGateUnmet, "qualification publication is not verified")
	}
	description := verified.release
	description.Artifacts = cloneArtifacts(verified.release.Artifacts)
	return description, nil
}

func (verified *verifiedQualificationPublication) Close() error {
	if verified == nil {
		return nil
	}
	verified.mu.Lock()
	defer verified.mu.Unlock()
	if !verified.valid {
		return nil
	}
	verified.valid = false
	return verified.publication.Close()
}

func (consumer *Consumer) qualificationMaterializer() qualificationMaterializer {
	if consumer == nil {
		return nil
	}
	if consumer.materializeChecked != nil {
		return consumer.materializeChecked
	}
	// Only package-private fixture Consumers omit the checked adapter. The public
	// constructor always installs it; a failed checked pull never falls back.
	if consumer.materialize != nil {
		return func(ctx context.Context, reference string) (qualificationPublication, error) {
			return consumer.materialize(ctx, reference)
		}
	}
	return nil
}

func (consumer *Consumer) verifyQualificationInputs(ctx context.Context, reference string, policy VerificationPolicy,
	expectation *ExactInputExpectation,
) (_ *verifiedQualificationPublication, _ ExactInputDescription, resultErr error) {
	if expectation == nil || !expectation.issued || expectation.prepared == nil || len(expectation.outputs) == 0 {
		return nil, ExactInputDescription{}, e.New(e.CodeGateUnmet, "exact-input expectation was not issued")
	}
	materialize := consumer.qualificationMaterializer()
	if err := consumer.authenticatePublication(ctx, reference, policy, materialize != nil); err != nil {
		return nil, ExactInputDescription{}, err
	}
	publication, err := materialize(ctx, reference)
	if err != nil {
		return nil, ExactInputDescription{}, err
	}
	transferred := false
	defer func() {
		if !transferred {
			if closeErr := publication.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, closeErr)
			}
		}
	}()
	release, err := describeVerifiedPublication(reference, policy, publication)
	if err != nil {
		return nil, ExactInputDescription{}, err
	}
	description, err := publication.Description(reference)
	if err != nil {
		return nil, ExactInputDescription{}, err
	}
	inputs, err := matchExactPublicationInputs(release, description, expectation)
	if err != nil {
		return nil, ExactInputDescription{}, err
	}
	transferred = true
	return &verifiedQualificationPublication{valid: true, release: release, publication: publication}, inputs, nil
}
